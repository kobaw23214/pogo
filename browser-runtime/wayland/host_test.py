"""Required security/correctness invariants, no real process/container access."""
import asyncio
import importlib.util
import io
from pathlib import Path
import unittest
from types import SimpleNamespace

spec = importlib.util.spec_from_file_location('wlhost', Path(__file__).with_name('host.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class Capture:
    def __init__(self): self.idrs = 0
    def request_idr_frame(self): self.idrs += 1


class ProbeTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.host = object.__new__(module.Host)
        self.host.output = io.StringIO()
        self.host.pages = {}
        self.host.frame_queue = asyncio.Queue(maxsize=16)
        self.host.stream_ready = asyncio.Event(); self.host.stream_ready.set()
        self.host.stats = {'encoded': 0, 'sent': 0, 'dropped': 0, 'gated': 0}
        self.page = {'page': 't1', 'capture': Capture(), 'fenced': False, 'clean_seq': 12, 'announced': False, 'needs_keyframe': True}
        self.host.pages['t1'] = self.page
        self.header = {'keyframe': True, 'content_seq': 12, 'seq': 8}

    def test_delayed_private_callback_cannot_escape_after_resume(self):
        self.host.enqueue(self.page, self.header, b'frame', True)
        self.assertTrue(self.host.frame_queue.empty())
        self.assertEqual(self.host.output.getvalue(), '')

    def test_marker_content_is_rejected_even_when_private_flag_late(self):
        self.host.enqueue(self.page, self.header | {'content_seq': 11}, b'frame', False)
        self.assertTrue(self.host.frame_queue.empty())
        self.assertEqual(self.host.output.getvalue(), '')

    def test_clean_keyframe_enters_authenticated_transport_queue(self):
        self.host.enqueue(self.page, self.header, b'frame', False)
        self.assertEqual(self.host.frame_queue.get_nowait()[2], b'frame')
        self.assertIn('frame_metadata', self.host.output.getvalue())

    def test_replaced_binding_cannot_emit(self):
        self.host.pages['t1'] = self.page | {'capture': Capture()}
        self.host.enqueue(self.page, self.header, b'frame', False)
        self.assertTrue(self.host.frame_queue.empty())

    def test_queue_drop_breaks_reference_chain_until_keyframe(self):
        self.page['needs_keyframe'] = False
        for _ in range(16): self.host.frame_queue.put_nowait((self.page, self.header, b'old'))
        self.host.enqueue(self.page, self.header | {'keyframe': False}, b'delta', False)
        self.assertEqual(self.host.stats['dropped'], 1)
        self.assertEqual(self.host.stats['gated'], 1)
        self.assertTrue(self.page['needs_keyframe'])
        self.assertGreater(self.page['capture'].idrs, 0)

    async def test_unrelated_sheet_release_preserves_active_button(self):
        calls=[]
        self.host.active_pointer_page='t1'
        self.page.update(window_id=1,buttons={272})
        other={'page':'t2','window_id':2,'buttons':set(),'generation':1}
        self.host.pages['t2']=other
        self.host.control=SimpleNamespace(inject_mouse_button=lambda *a:calls.append(('up',a)))
        def native(op,**args):
            calls.append((op,args))
            return {'window_id':1,'grab_window_id':1} if op=='pointer_target' else {'ack':True}
        self.host.native=native
        await self.host.release({'page':'t2'})
        self.assertEqual(self.page['buttons'],{272});self.assertEqual(self.host.active_pointer_page,'t1')
        self.assertFalse(any(c[0]=='up' for c in calls))
        self.assertEqual(calls[-1],('release',{'window_id':2}))
        await self.host.release({'page':'t1'})
        self.assertEqual(self.page['buttons'],set());self.assertIsNone(self.host.active_pointer_page)
        self.assertIn(('up',(272,0)),calls);self.assertEqual(calls[-1],('release',{'window_id':1}))

    async def test_vp8_callback_uses_stock_codec_tag_and_freezes_source(self):
        class FakeCapture(Capture):
            def start_capture(self, callback, settings): self.callback = callback
            def capture_state(self, output): return ('running', None)
        class FakeFrame:
            content_seq=12; capture_ns=1; encode_start_ns=2; encode_end_ns=3; frame_id=1; reference_frame_id=-1
            def __init__(self, tag): self.data_type=tag
            def __len__(self): return 10
            def __bytes__(self): return b'\x00\x00\x00\x9d\x01\x2a\x40\x06\xe8\x03'
        self.host.loop=asyncio.get_running_loop();self.host.network='lan'
        self.host.ScreenCapture=FakeCapture;self.host.CaptureSettings=SimpleNamespace
        self.host.control=SimpleNamespace(get_realized_geometry=lambda output:(1600,1000,1.25))
        self.page.update(capture=None,generation=1,display_id=2,pixel_width=1600,pixel_height=1000,scale=1.25,width=1280,height=800,window_id=1,seq=0)
        await self.host.capture_start({'page':'t1','generation':1})
        owner=self.page['capture']
        owner.callback(FakeFrame(1))  # H264 must not enter a VP8 stream.
        owner.callback(FakeFrame(2))
        await asyncio.sleep(0)
        _,h,_=self.host.frame_queue.get_nowait()
        self.assertEqual(h['codec'],'vp8');self.assertEqual(h['width'],1600)
        self.page['capture']=FakeCapture()
        owner.callback(FakeFrame(2))  # stopped encoder callback cannot attach to its replacement.
        await asyncio.sleep(0)
        self.assertTrue(self.host.frame_queue.empty())

    async def unbind_fixture(self, present, destroyed=True, native_error=None):
        calls=[]
        self.host.active_pointer_page=None
        self.page.update(window_id=1,buttons=set(),generation=1,display_id=2,capture=None)
        def native(op,**args):
            calls.append((op,args))
            if op=='pointer_target':return {'window_id':None,'grab_window_id':None}
            if op=='release':return {'ack':True}
            if op=='park_if_present':
                if native_error:raise native_error
                return {'ack':True,'present':present}
            raise AssertionError('cleanup must not use separate windows/park')
        self.host.native=native
        self.host.control=SimpleNamespace(destroy_output=lambda output:calls.append(('destroy_output',output)) or destroyed)
        return calls

    async def test_unbind_closed_window_uses_one_atomic_presence_cleanup(self):
        calls=await self.unbind_fixture(False)
        self.assertEqual(await self.host.unbind({'page':'t1'}),{'ack':True})
        self.assertNotIn('t1',self.host.pages)
        self.assertEqual([c[0] for c in calls],['pointer_target','release','park_if_present','destroy_output'])

    async def test_unbind_live_window_parks_before_destroying_output(self):
        calls=await self.unbind_fixture(True)
        await self.host.unbind({'page':'t1'})
        self.assertEqual(calls[-2:], [('park_if_present',{'window_id':1}),('destroy_output',2)])

    async def test_unbind_does_not_confirm_failed_output_destruction(self):
        await self.unbind_fixture(False,destroyed=False)
        with self.assertRaisesRegex(RuntimeError,'output destruction was not acknowledged'):
            await self.host.unbind({'page':'t1'})
        self.assertIs(self.host.pages['t1'],self.page)
        self.assertTrue(self.page['fenced'])

    async def test_unbind_does_not_swallow_unrelated_native_failure(self):
        calls=await self.unbind_fixture(False,native_error=RuntimeError('native control timed out'))
        with self.assertRaisesRegex(RuntimeError,'native control timed out'):
            await self.host.unbind({'page':'t1'})
        self.assertIs(self.host.pages['t1'],self.page)
        self.assertFalse(any(c[0]=='destroy_output' for c in calls))

    def test_geometry_matches_exact_css_and_physical_limits(self):
        self.assertEqual(module.geometry({'width':1280,'height':800,'scale':1.25}), (1280,800,1.25,1600,1000))
        for v in ({'width':1281,'height':800,'scale':1.25}, {'width':2560,'height':1600,'scale':2}, {'width':True,'height':800}):
            with self.assertRaises(ValueError): module.geometry(v)


if __name__ == '__main__': unittest.main()
