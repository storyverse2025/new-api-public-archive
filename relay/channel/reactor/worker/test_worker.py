import asyncio
import tempfile
import unittest
from pathlib import Path
from worker import Store, validate_request, validate_public_url, generate

class ValidationTests(unittest.TestCase):
    def test_exact_duration_and_zero_seed(self):
        value = validate_request({'model':'reactor/fast-h3-omni','prompt':'scene','seconds':5.875,'seed':0})
        self.assertEqual(value['seed'],0)
        self.assertEqual(value['seconds'],5.875)
    def test_reject_invalid_duration_references_and_unknown_fields(self):
        for extra in ({'seconds':6},{'reference_audios':['https://example.com/a.mp3']},{'first_frame_image':'https://example.com/a.png'},{'resolution':'720p'},{'model':'other'},{'reference_images':['https://example.com/a']*10}):
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                validate_request({'model':'reactor/fast-h3-omni','prompt':'scene',**extra})
    def test_reject_private_and_non_https_media(self):
        for url in ('file:///etc/passwd','http://example.com/a','https://127.0.0.1/a','https://[::1]/a','https://169.254.169.254/latest','https://user:pass@example.com/a'):
            with self.subTest(url=url),self.assertRaises(ValueError):validate_public_url(url)
    def test_store_persists_and_marks_interrupted_tasks_failed(self):
        with tempfile.TemporaryDirectory() as directory:
            store=Store(Path(directory));task=store.create({'prompt':'scene','seconds':5.875})
            store.update(task,'in_progress',session_id='sess_abc')
            reopened=Store(Path(directory))
            self.assertEqual(reopened.get(task)['status'],'failed')
            self.assertFalse(reopened.get(task)['output_ready'])
            self.assertNotIn('api_key',reopened.get(task))

class FailingReactor:
    disconnected=False
    def __init__(self,*args,**kwargs):self.session_id='sess_abc';self.status='ready'
    def on(self,*args):pass
    async def connect(self):pass
    async def send_command(self,*args):raise RuntimeError('command refused')
    async def disconnect(self):type(self).disconnected=True

class LifecycleTests(unittest.IsolatedAsyncioTestCase):
    async def test_generation_failure_disconnects_and_persists_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            store=Store(Path(directory));request=validate_request({'model':'reactor/fast-h3-omni','prompt':'scene','seconds':5.875});task=store.create(request)
            await generate(store,task,request,'test-secret',reactor_factory=FailingReactor)
            self.assertTrue(FailingReactor.disconnected)
            self.assertEqual(store.get(task)['status'],'failed')
            self.assertGreaterEqual(store.get(task)['data']['duration_seconds'],0)
            self.assertNotIn('test-secret',str(store.get(task)))


class SuccessfulReactor:
    disconnected=False
    commands=[]
    def __init__(self,*args,**kwargs):self.session_id='sess_abc';self.status='ready';self.handlers={};self.tracks={}
    def track(self,name):
        reactor=self
        class Track:
            def on_raw_frame(self,callback):reactor.tracks[name]=callback
        return Track()
    def on(self,name,handler):self.handlers[name]=handler
    async def connect(self):self.handlers['status_changed']('ready')
    async def send_command(self,name,data):
        type(self).commands.append((name,data))
        if name=='enqueue':
            self.handlers['message']({'type':'clip_generated','data':{'clip':{'clip_id':'clip_abc'}}})
            return {'type':'clip_queued','data':{'clip':{'clip_id':'clip_abc'}}}
        if name=='play':
            self.handlers['message']({'type':'clip_started','data':{'clip':{'clip_id':'clip_abc'}}})
            for index in range(141):self.tracks['main_video'](bytes([255,0,0,255])*64*64,64,64,index,index*41667,b'')
            self.tracks['main_audio'](b'\x00\x10'*282000,282000,48000,1)
            self.handlers['message']({'type':'clip_finished','data':{'clip':{'clip_id':'clip_abc'}}})
    async def request_clip(self,seconds):return {'seconds':seconds}
    async def download(self,clip,path,**kwargs):
        import subprocess
        subprocess.run(['ffmpeg','-nostdin','-loglevel','error','-y','-f','lavfi','-i','color=c=blue:s=64x64:r=24','-f','lavfi','-i','sine=frequency=440:sample_rate=48000','-t','0.5','-c:v','libx264','-c:a','aac','-f','mpegts',str(path)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    async def disconnect(self):type(self).disconnected=True

class SuccessLifecycleTests(unittest.IsolatedAsyncioTestCase):
    async def test_generation_exports_mp4_before_completed_and_disconnects(self):
        import shutil
        if not shutil.which('ffmpeg'):self.skipTest('ffmpeg not installed')
        with tempfile.TemporaryDirectory() as directory:
            store=Store(Path(directory));request=validate_request({'model':'reactor/fast-h3-omni','prompt':'scene','seconds':5.875,'seed':0});task=store.create(request)
            await generate(store,task,request,'test-secret',reactor_factory=SuccessfulReactor)
            result=store.get(task)
            self.assertEqual(result['status'],'completed',result)
            self.assertTrue(result['output_ready'])
            self.assertTrue(SuccessfulReactor.disconnected)
            self.assertFalse((Path(directory)/(task+'.ts')).exists())
            enqueue=next(data for name,data in SuccessfulReactor.commands if name=='enqueue')
            self.assertEqual(enqueue['seed'],0)
            self.assertEqual(enqueue['seconds'],5.875)
            self.assertGreater(result['data']['duration_seconds'],0)

class WorkerHTTPTests(unittest.TestCase):
    def test_private_worker_authentication_status_and_mp4_content(self):
        import json,os,socket,subprocess,time,urllib.error,urllib.request
        with tempfile.TemporaryDirectory() as directory:
            store=Store(Path(directory));task=store.create({'seconds':5.875,'prompt':'scene'});store.output(task).write_bytes(b'test-video');store.update(task,'completed',duration=17.25)
            with socket.socket() as sock:sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
            secret='unit-test-worker-secret-32-characters'
            environment={**os.environ,'REACTOR_API_KEY':'test-upstream-key','REACTOR_WORKER_SECRET':secret,'REACTOR_WORKER_HOST':'127.0.0.1','REACTOR_WORKER_PORT':str(port),'REACTOR_WORKER_DATA_DIR':directory}
            process=subprocess.Popen([__import__('sys').executable,str(Path(__file__).with_name('worker.py'))],env=environment,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            try:
                url=f'http://127.0.0.1:{port}/tasks/{task}'
                for _ in range(60):
                    try:urllib.request.urlopen(url,timeout=1)
                    except urllib.error.HTTPError as error:
                        self.assertEqual(error.code,401);error.close();break
                    except OSError:time.sleep(.05)
                else:self.fail('worker did not start')
                headers={'Authorization':'Bearer '+secret}
                with urllib.request.urlopen(urllib.request.Request(url,headers=headers)) as response:result=json.load(response)
                self.assertEqual(result['data']['duration_seconds'],17.25)
                self.assertTrue(result['output_ready'])
                self.assertNotIn(secret,str(result))
                with urllib.request.urlopen(urllib.request.Request(url+'/content',headers=headers)) as response:
                    self.assertEqual(response.headers['Content-Type'],'video/mp4');self.assertEqual(response.read(),b'test-video')
                bad=urllib.request.Request(f'http://127.0.0.1:{port}/tasks',data=b'{"model":"other","prompt":"scene"}',headers=headers,method='POST')
                with self.assertRaises(urllib.error.HTTPError) as raised:urllib.request.urlopen(bad)
                self.assertEqual(raised.exception.code,400);raised.exception.close()
            finally:
                process.terminate();process.wait(timeout=5)

class CaptureTests(unittest.TestCase):
    def test_capture_bounds_raw_frames_and_encodes_audio_video(self):
        from worker import Capture
        import json,subprocess
        with tempfile.TemporaryDirectory() as directory:
            capture=Capture(Path(directory),'capture',0.5,'1:1')
            capture.start()
            for index in range(20):capture.video(bytes([255,0,0,255])*64*64,64,64,index,index*41667,b'')
            capture.audio(b'\x00\x10'*30000,30000,48000,1)
            capture.finish(Path(directory)/'output.mp4')
            probe=json.loads(subprocess.check_output(['ffprobe','-v','error','-show_streams','-show_format','-of','json',str(Path(directory)/'output.mp4')]))
            self.assertEqual({stream['codec_type'] for stream in probe['streams']},{'video','audio'})
            self.assertAlmostEqual(float(probe['format']['duration']),.5,delta=.08)
            self.assertEqual(capture.frames,12)
            self.assertFalse((Path(directory)/'capture.bgra').exists())
    def test_capture_failure_removes_spool_files(self):
        from worker import Capture
        with tempfile.TemporaryDirectory() as directory:
            capture=Capture(Path(directory),'capture',5.875,'16:9');capture.start()
            with self.assertRaises(ValueError):capture.finish(Path(directory)/'output.mp4')
            self.assertEqual(list(Path(directory).glob('*.bgra')),[])
            self.assertEqual(list(Path(directory).glob('*.pcm')),[])



class CaptureTimestampTests(unittest.TestCase):
    def test_zero_timestamps_preserve_distinct_frames(self):
        from worker import Capture
        with tempfile.TemporaryDirectory() as directory:
            capture=Capture(Path(directory),'zero',.5,'1:1');capture.start()
            first=bytes([0,0,255,255])*4
            second=bytes([255,0,0,255])*4
            capture.video(first,2,2,100,0,b'')
            capture.video(second,2,2,101,0,b'')
            capture.video(first,2,2,102,0,b'')
            capture.video_file.flush()
            self.assertEqual(capture.frames,3)
            self.assertEqual(capture.video_path.read_bytes(),first+second+first)
            capture.close()
    def test_nonadvancing_metadata_uses_callback_order(self):
        from worker import Capture
        with tempfile.TemporaryDirectory() as directory:
            capture=Capture(Path(directory),'same',.5,'1:1');capture.start()
            first=bytes([0,0,255,255])*4
            second=bytes([255,0,0,255])*4
            capture.video(first,2,2,0,1000,b'')
            capture.video(second,2,2,0,1000,b'')
            capture.video_file.flush()
            self.assertEqual(capture.frames,2)
            self.assertEqual(capture.video_path.read_bytes(),first+second)
            capture.close()



class CaptureMixedMetadataTests(unittest.TestCase):
    def test_sparse_timestamp_updates_do_not_drop_fallback_frames(self):
        from worker import Capture
        with tempfile.TemporaryDirectory() as directory:
            capture=Capture(Path(directory),'mixed',.5,'1:1');capture.start()
            try:
                for index,timestamp in enumerate((0,0,1,2)):
                    capture.video(bytes([index,0,0,255])*4,2,2,0,timestamp,b'')
                self.assertEqual(capture.frames,4)
            finally:capture.close()

if __name__=='__main__':unittest.main()
