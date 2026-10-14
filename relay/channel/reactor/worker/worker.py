"""Private async video bridge for Reactor's official WebRTC SDK.

This is an internal gateway service, not a Reactor REST API. API credentials never
enter task payloads or SQLite. Each job owns one bounded, nonrecoverable session.
"""
import asyncio
import hmac
import http.client
import ipaddress
import json
import mimetypes
import os
import re
import socket
import signal
import sqlite3
import ssl
import subprocess
import threading
import time
import urllib.parse
import urllib.request
import uuid
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

MODEL = 'reactor/fast-h3-omni'
SECONDS = (5.875, 8, 10.125, 12.25, 14.375)
MAX_FILE_BYTES = 25 * 1024 * 1024
SESSION_MAX_SECONDS = min(120, max(1, int(os.environ.get('REACTOR_SESSION_MAX_SECONDS', '120'))))
TASK_ID = re.compile(r'^reactor_[a-f0-9]{32}$')


def validate_request(request):
    allowed = {'model','prompt','seconds','canvas','reference_images','reference_audios','first_frame_image','last_frame_image','seed'}
    if not isinstance(request,dict) or set(request)-allowed:
        raise ValueError('unsupported request parameters')
    value = {'model':MODEL,'seconds':14.375,'canvas':'16:9',**request}
    if value['model'] != MODEL:
        raise ValueError('unsupported model')
    if not isinstance(value.get('prompt'),str) or not value['prompt'].strip() or len(value['prompt'].encode()) > 16000:
        raise ValueError('prompt is required and must be at most 16000 bytes')
    if isinstance(value['seconds'],bool) or value['seconds'] not in SECONDS:
        raise ValueError('seconds must be 5.875, 8, 10.125, 12.25 or 14.375')
    if value['canvas'] not in ('16:9','1:1','9:16','4:3'):
        raise ValueError('unsupported canvas')
    if 'seed' in value and (isinstance(value['seed'],bool) or not isinstance(value['seed'],int)):
        raise ValueError('seed must be an integer')
    for key,maximum in (('reference_images',9),('reference_audios',3)):
        items = value.get(key,[])
        if not isinstance(items,list) or len(items)>maximum or any(not isinstance(x,str) or not x for x in items):
            raise ValueError('invalid '+key)
    if not value.get('reference_images') and (value.get('reference_audios') or value.get('first_frame_image') or value.get('last_frame_image')):
        raise ValueError('audio and keyframes require a reference image')
    for key in ('first_frame_image','last_frame_image'):
        if key in value and (not isinstance(value[key],str) or not value[key]):
            raise ValueError('keyframes must be image URLs')
    for raw in value.get('reference_images',[])+value.get('reference_audios',[])+[value[k] for k in ('first_frame_image','last_frame_image') if value.get(k)]:
        parsed=urllib.parse.urlsplit(raw)
        if parsed.scheme!='https' or not parsed.hostname or parsed.username or parsed.password:
            raise ValueError('references must be public HTTPS URLs')
    return value


def validate_public_url(raw):
    parsed=urllib.parse.urlsplit(raw)
    if parsed.scheme!='https' or not parsed.hostname or parsed.username or parsed.password or parsed.port not in (None,443):
        raise ValueError('references must be public HTTPS URLs on port 443')
    addresses={item[4][0] for item in socket.getaddrinfo(parsed.hostname,443,type=socket.SOCK_STREAM)}
    if not addresses or any(not ipaddress.ip_address(address).is_global for address in addresses):
        raise ValueError('private and reserved media addresses are forbidden')
    return parsed, sorted(addresses)[0]


class PinnedHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self,host,address):
        super().__init__(host,443,timeout=30,context=ssl.create_default_context())
        self.address=address
    def connect(self):
        sock=socket.create_connection((self.address,443),timeout=self.timeout)
        self.sock=self._context.wrap_socket(sock,server_hostname=self.host)


def download_reference(raw,kind):
    """Resolve every redirect and pin the validated IP while preserving TLS SNI."""
    for _ in range(4):
        parsed,address=validate_public_url(raw)
        connection=PinnedHTTPSConnection(parsed.hostname,address)
        try:
            path=urllib.parse.urlunsplit(('', '', parsed.path or '/',parsed.query,''))
            connection.request('GET',path,headers={'Host':parsed.hostname,'Accept':'*/*'})
            response=connection.getresponse()
            if response.status in (301,302,303,307,308):
                location=response.getheader('Location')
                if not location:raise ValueError('invalid media redirect')
                raw=urllib.parse.urljoin(raw,location)
                continue
            if response.status!=200:raise ValueError('reference download failed')
            size=response.getheader('Content-Length')
            if size and int(size)>MAX_FILE_BYTES:raise ValueError('reference exceeds 25 MiB')
            data=response.read(MAX_FILE_BYTES+1)
            if not data or len(data)>MAX_FILE_BYTES:raise ValueError('reference is empty or exceeds 25 MiB')
            mime=(response.getheader('Content-Type') or '').split(';',1)[0].lower()
            name=Path(parsed.path).name or ('reference.png' if kind=='image' else 'reference.wav')
            guessed=mimetypes.guess_type(name)[0]
            if mime in ('','application/octet-stream'):mime=guessed
            allowed={'image/jpeg','image/png','image/webp'} if kind=='image' else {'audio/wav','audio/x-wav','audio/mpeg','audio/mp3','audio/aac','audio/mp4','audio/ogg','audio/opus','audio/flac','audio/x-flac','audio/webm','video/webm','video/mp4','application/ogg'}
            if mime not in allowed:raise ValueError('unsupported reference media type')
            return data,name,mime
        finally:connection.close()
    raise ValueError('too many reference redirects')


class Store:
    def __init__(self,directory):
        self.directory=Path(directory)
        self.directory.mkdir(parents=True,exist_ok=True,mode=0o700)
        self.db=self.directory/'tasks.sqlite3'
        with self.connection() as connection:
            connection.execute('CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, status TEXT, request TEXT, error TEXT DEFAULT "", session_id TEXT DEFAULT "", duration REAL DEFAULT 0, created REAL, phase TEXT DEFAULT "queued", events TEXT DEFAULT "[]")')
            columns={row[1] for row in connection.execute('PRAGMA table_info(tasks)')}
            if 'phase' not in columns:connection.execute('ALTER TABLE tasks ADD COLUMN phase TEXT DEFAULT "queued"')
            if 'events' not in columns:connection.execute('ALTER TABLE tasks ADD COLUMN events TEXT DEFAULT "[]"')
            connection.execute('UPDATE tasks SET status="failed", error="worker restarted before completion" WHERE status IN ("queued","in_progress")')
        os.chmod(self.db,0o600)
    @contextmanager
    def connection(self):
        connection=sqlite3.connect(self.db,timeout=30)
        try:
            with connection:yield connection
        finally:connection.close()
    def create(self,request):
        task='reactor_'+uuid.uuid4().hex
        with self.connection() as connection:
            connection.execute('INSERT INTO tasks (id,status,request,created) VALUES (?,?,?,?)',(task,'queued',json.dumps(request),time.time()))
        return task
    def update(self,task,status,error='',session_id=None,duration=None,phase=None):
        with self.connection() as connection:
            connection.execute('UPDATE tasks SET status=?,error=?,session_id=COALESCE(?,session_id),duration=COALESCE(?,duration),phase=COALESCE(?,phase) WHERE id=?',(status,error,session_id,duration,phase,task))
    def event(self,task,event):
        with self.connection() as connection:
            row=connection.execute('SELECT events FROM tasks WHERE id=?',(task,)).fetchone()
            if row:
                events=(json.loads(row[0])+[event])[-20:]
                connection.execute('UPDATE tasks SET events=? WHERE id=?',(json.dumps(events),task))
    def get(self,task):
        with self.connection() as connection:
            row=connection.execute('SELECT status,request,error,duration,created,phase,events FROM tasks WHERE id=?',(task,)).fetchone()
        if not row:return None
        status,request,error,duration,created,phase,events=row
        return {'task_id':task,'status':status,'output_ready':status=='completed' and self.output(task).is_file(),'error':error,'created_at':int(created),'data':{'duration_seconds':duration,'output_duration_seconds':json.loads(request)['seconds'],'phase':phase,'events':json.loads(events)}}
    def output(self,task):return self.directory/(task+'.mp4')


def safe_error(error,key):
    message=str(error).replace(key,'[redacted]') if key else str(error)
    message=re.sub(r'(?:rk_|eyJ)[A-Za-z0-9_.-]+','[redacted]',message)
    if len(message)>1000:message=message[:250]+' ... '+message[-700:]
    return message or type(error).__name__


class Capture:
    """Bounded disk spool for the official SDK's decoded BGRA and i16 PCM.

    RTP presentation timestamps place frames on the model's native 24 fps clock.
    Missing receive frames repeat the prior frame; no unbounded Python queue or
    frame list is used. Each spool is bounded by the requested clip duration.
    """
    def __init__(self,directory,task,seconds,canvas):
        self.video_path=Path(directory)/(task+'.bgra')
        self.audio_path=Path(directory)/(task+'.pcm')
        self.seconds=seconds
        self.max_frames=round(seconds*24)
        self.max_audio=round(seconds*48000)*2
        self.frames=0
        self.audio_bytes=0
        self.first_timestamp=None
        self.last_timestamp=None
        self.first_frame_id=None
        self.last_frame_id=None
        self.last_frame=None
        self.width=self.height=0
        self.active=False
        self.error=None
        self.lock=threading.Lock()
        self.video_file=self.video_path.open('wb')
        self.audio_file=self.audio_path.open('wb')
    def start(self):
        with self.lock:self.active=True
    def video(self,data,width,height,frame_id,timestamp_us,user_data):
        with self.lock:
            if not self.active or self.frames>=self.max_frames:return
            if width<=0 or height<=0 or width*height>1344*1344 or len(data)!=width*height*4:
                self.error='invalid decoded video frame';return
            if self.width and (self.width!=width or self.height!=height):
                self.error='video canvas changed during capture';return
            self.width,self.height=width,height
            if self.first_frame_id is None:self.first_frame_id=frame_id
            # The SDK reports zero when the frame has no capture metadata.
            # Equal/nonadvancing stamps must not collapse a whole clip to frame 0.
            if timestamp_us>0 and (self.last_timestamp is None or timestamp_us>self.last_timestamp):
                if self.first_timestamp is None:
                    self.first_timestamp=timestamp_us-self.frames*1000000/24
                target=max(self.frames,round((timestamp_us-self.first_timestamp)*24/1000000))
            elif self.last_frame_id is not None and frame_id>self.last_frame_id:
                target=max(self.frames,frame_id-self.first_frame_id)
            else:
                target=self.frames
            self.last_timestamp=timestamp_us
            self.last_frame_id=frame_id
            target=min(self.max_frames-1,target)
            if target<self.frames:return
            while self.frames<target and self.last_frame is not None:
                self.video_file.write(self.last_frame);self.frames+=1
            self.video_file.write(data);self.frames+=1;self.last_frame=data
    def audio(self,data,num_samples,sample_rate,num_channels):
        with self.lock:
            if not self.active or self.audio_bytes>=self.max_audio:return
            if sample_rate!=48000 or num_channels!=1:
                self.error='expected native 48 kHz mono audio';return
            chunk=data[:self.max_audio-self.audio_bytes]
            self.audio_file.write(chunk);self.audio_bytes+=len(chunk)
    @property
    def complete(self):
        with self.lock:return self.frames>=self.max_frames and self.audio_bytes>=self.max_audio
    def close(self):
        with self.lock:
            self.active=False
            self.video_file.close();self.audio_file.close()
        self.video_path.unlink(missing_ok=True);self.audio_path.unlink(missing_ok=True)
    def finish(self,destination):
        temporary=destination.with_suffix('.partial.mp4')
        try:
            with self.lock:
                self.active=False
                if self.error:raise ValueError(self.error)
                if not self.frames or not self.audio_bytes:raise ValueError('playback delivered no video or audio frames')
                while self.frames<self.max_frames:
                    self.video_file.write(self.last_frame);self.frames+=1
                if self.audio_bytes<self.max_audio:self.audio_file.write(bytes(self.max_audio-self.audio_bytes))
                self.video_file.close();self.audio_file.close()
            subprocess.run(['ffmpeg','-nostdin','-hide_banner','-loglevel','error','-y','-f','rawvideo','-pixel_format','bgra','-video_size',f'{self.width}x{self.height}','-framerate','24','-i',str(self.video_path),'-f','s16le','-ar','48000','-ac','1','-i',str(self.audio_path),'-t',str(self.seconds),'-c:v','libx264','-preset','veryfast','-crf','18','-pix_fmt','yuv420p','-c:a','aac','-b:a','128k','-movflags','+faststart',str(temporary)],check=True,timeout=30,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            if not temporary.stat().st_size:raise ValueError('exported video is empty')
            temporary.replace(destination)
        finally:
            self.close();temporary.unlink(missing_ok=True)


async def generate(store,task,request,api_key,reactor_factory=None):
    try:
        if reactor_factory is None:
            from reactor_sdk import Reactor
            reactor_factory=Reactor
        reactor=reactor_factory(MODEL,api_key=api_key,max_session_duration_seconds=SESSION_MAX_SECONDS)
    except Exception as error:
        store.update(task,'failed',error=safe_error(error,api_key))
        return
    loop=asyncio.get_running_loop()
    messages=asyncio.Queue()
    ready_at=None
    capture=Capture(store.directory,task,request['seconds'],request['canvas'])
    def status_changed(status):
        nonlocal ready_at
        if status=='ready' and ready_at is None:ready_at=time.monotonic()
    def message_received(message):
        store.event(task,str(message.get('type','unknown'))[:80])
        if message.get('type')=='clip_started':capture.start()
        loop.call_soon_threadsafe(messages.put_nowait,message)
    reactor.on('status_changed',status_changed)
    reactor.on('message',message_received)
    async def wait_message(expected,clip_id):
        while True:
            message=await messages.get()
            data=message.get('data') or {}
            kind=message.get('type')
            if kind=='command_error':raise RuntimeError(data.get('reason') or data.get('message') or 'model refused command')
            actual=data.get('clip_id') or (data.get('clip') or {}).get('clip_id')
            if kind in ('clip_failed','clip_generation_failed') and (not actual or actual==clip_id):
                raise RuntimeError(data.get('reason') or 'clip generation failed')
            if kind==expected and actual==clip_id:return data
    async def run_session():
        nonlocal ready_at
        store.update(task,'in_progress',phase='configuring')
        await reactor.send_command('set_canvas',{'aspect':request['canvas']})
        await reactor.send_command('set_autoplay',{'enabled':False})
        await reactor.send_command('set_flush_on_clip_end',{'enabled':False})
        reactor.track('main_video').on_raw_frame(capture.video)
        reactor.track('main_audio').on_raw_frame(capture.audio)
        uploaded={}
        async def upload(raw,kind):
            if raw not in uploaded:
                data,name,mime=await asyncio.to_thread(download_reference,raw,kind)
                uploaded[raw]=await reactor.upload_file(data,name=name,mime_type=mime)
            return uploaded[raw]
        enqueue={'prompt':request['prompt'],'seconds':request['seconds']}
        if 'seed' in request:enqueue['seed']=request['seed']
        store.update(task,'in_progress',phase='uploading')
        for field,kind in (('reference_images','image'),('reference_audios','audio')):
            if request.get(field):
                enqueue[field]=[]
                for raw in request[field]:
                    enqueue[field].append(await upload(raw,kind))
        for field in ('first_frame_image','last_frame_image'):
            if request.get(field):
                enqueue[field]=await upload(request[field],'image')
        store.update(task,'in_progress',phase='generating')
        reply=await reactor.send_command('enqueue',enqueue)
        reply_data=(reply or {}).get('data',{})
        clip_id=reply_data.get('clip_id') or (reply_data.get('clip') or {}).get('clip_id')
        if not clip_id:raise RuntimeError('model did not acknowledge a queued clip')
        await wait_message('clip_generated',clip_id)
        store.update(task,'in_progress',phase='playing')
        await reactor.send_command('play',{'clip_id':clip_id})
        await wait_message('clip_finished',clip_id)
        store.update(task,'in_progress',phase='exporting')
        for _ in range(20):
            if capture.complete:break
            await asyncio.sleep(.1)
        await asyncio.to_thread(capture.finish,store.output(task))
    status='failed'
    error=''
    try:
        store.update(task,'in_progress',phase='connecting')
        await asyncio.wait_for(reactor.connect(),timeout=120)
        if ready_at is None:ready_at=time.monotonic()
        store.update(task,'in_progress',session_id=reactor.session_id)
        await asyncio.wait_for(run_session(),timeout=SESSION_MAX_SECONDS)
        status='completed'
    except Exception as exception:
        error=safe_error(exception,api_key)
        if isinstance(exception,TimeoutError):error='operation timed out during '+store.get(task)['data']['phase']
    finally:
        try:await asyncio.wait_for(reactor.disconnect(),timeout=10)
        except Exception:
            # Independent API cleanup complements the server-side lifetime ceiling.
            if reactor.session_id:
                await asyncio.to_thread(terminate_session,reactor.session_id,api_key)
        if hasattr(reactor,'close'):reactor.close()
        capture.close()
        duration=0 if ready_at is None else max(0,time.monotonic()-ready_at)
        store.update(task,status,error=error,duration=duration)
        if status!='completed':store.output(task).unlink(missing_ok=True)


def terminate_session(session_id,key):
    request=urllib.request.Request('https://api.reactor.inc/sessions/'+urllib.parse.quote(session_id,safe=''),headers={'Authorization':'Bearer '+key},method='DELETE')
    try:
        with urllib.request.urlopen(request,timeout=10):pass
    except Exception:pass


def main():
    api_key=os.environ.get('REACTOR_API_KEY','')
    secret=os.environ.get('REACTOR_WORKER_SECRET','')
    if not api_key or len(secret)<32:raise SystemExit('REACTOR_API_KEY and REACTOR_WORKER_SECRET (32+ characters) are required')
    store=Store(Path(os.environ.get('REACTOR_WORKER_DATA_DIR','/data/reactor')))
    executor=ThreadPoolExecutor(max_workers=max(1,min(4,int(os.environ.get('REACTOR_WORKER_CONCURRENCY','1')))))
    slots=threading.BoundedSemaphore(20)
    active={}
    active_lock=threading.Lock()
    def work(task,request):
        async def job():
            loop=asyncio.get_running_loop()
            current=asyncio.current_task()
            with active_lock:active[task]=(loop,current)
            try:await generate(store,task,request,api_key)
            finally:
                with active_lock:active.pop(task,None)
        try:asyncio.run(job())
        finally:slots.release()
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def authenticated(self):return hmac.compare_digest(self.headers.get('Authorization',''),'Bearer '+secret)
        def reply(self,status,payload):
            data=json.dumps(payload).encode()
            self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
        def do_POST(self):
            if not self.authenticated():return self.reply(401,{'error':'unauthorized'})
            if self.path!='/tasks':return self.reply(404,{'error':'not found'})
            try:
                length=int(self.headers.get('Content-Length','0'))
                if not 0<length<=65536:raise ValueError('request body must be 1..65536 bytes')
                request=validate_request(json.loads(self.rfile.read(length)))
            except (ValueError,TypeError):return self.reply(400,{'error':'invalid request parameters'})
            if not slots.acquire(blocking=False):return self.reply(429,{'error':'generation queue is full'})
            try:
                task=store.create(request);executor.submit(work,task,request)
                return self.reply(202,store.get(task))
            except Exception:
                slots.release();return self.reply(500,{'error':'could not create task'})
        def do_GET(self):
            if not self.authenticated():return self.reply(401,{'error':'unauthorized'})
            parts=self.path.split('/')
            if len(parts) not in (3,4) or parts[1]!='tasks' or not TASK_ID.fullmatch(parts[2]):return self.reply(404,{'error':'not found'})
            task=store.get(parts[2])
            if not task:return self.reply(404,{'error':'task not found'})
            if len(parts)==3:return self.reply(200,task)
            if parts[3]!='content' or not task['output_ready']:return self.reply(404,{'error':'video not ready'})
            with store.output(parts[2]).open('rb') as source:
                self.send_response(200);self.send_header('Content-Type','video/mp4');self.send_header('Content-Length',str(os.fstat(source.fileno()).st_size));self.send_header('Cache-Control','private, no-store');self.end_headers()
                while chunk:=source.read(65536):self.wfile.write(chunk)
    server=ThreadingHTTPServer((os.environ.get('REACTOR_WORKER_HOST','0.0.0.0'),int(os.environ.get('REACTOR_WORKER_PORT','8091'))),Handler)
    def stop(signum,frame):
        with active_lock:
            for loop,job in active.values():loop.call_soon_threadsafe(job.cancel)
        threading.Thread(target=server.shutdown,daemon=True).start()
    signal.signal(signal.SIGTERM,stop)
    signal.signal(signal.SIGINT,stop)
    try:server.serve_forever()
    except KeyboardInterrupt:pass
    finally:
        server.server_close();executor.shutdown(wait=True,cancel_futures=True)

if __name__=='__main__':main()
