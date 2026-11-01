#!/usr/bin/env python3
"""Session-private software Wayland compositor for Pogo. JSONL control + VP8 UDS.

Chrome is launched by Node into the returned private Wayland socket. There is no
screen sharing fallback: every Sheet is bound to a verified independent window.
"""
import argparse
import asyncio
import json
import math
import os
from pathlib import Path
import re
import struct
import sys
import time

IDENT = re.compile(r'^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$')
MAX_LINE = 65536
MAX_PAYLOAD = 8 * 1024 * 1024
VP8_DATA_TYPE = 2  # pixelflux Codec::Vp8.id()/StripeFrame.data_type


def identifier(value):
    if not isinstance(value, str) or not IDENT.fullmatch(value):
        raise ValueError('invalid identifier')
    return value


def geometry(args):
    w, h, r = args['width'], args['height'], args.get('scale', 1.25)
    if isinstance(w, bool) or isinstance(h, bool) or not isinstance(w, int) or not isinstance(h, int):
        raise ValueError('CSS dimensions must be integers')
    if not isinstance(r, (int, float)) or not math.isfinite(r) or r not in (1, 1.25, 1.5, 2):
        raise ValueError('unsupported scale')
    if not 64 <= w <= 3840 or not 64 <= h <= 2400:
        raise ValueError('CSS geometry is outside limits')
    pw, ph = round(w * r), round(h * r)
    if pw % 2 or ph % 2 or pw > 3840 or ph > 2400 or pw * ph > 7464960:
        raise ValueError('physical dimensions must be even and within 3840x2400')
    if pw / r != w or ph / r != h:
        raise ValueError('scale does not give exact CSS geometry')
    return w, h, float(r), pw, ph


class Host:
    def __init__(self, cfg, output):
        self.cfg, self.output = cfg, output
        self.capability = os.environ.pop('POGO_WAYLAND_SESSION_CAP', '')
        if len(self.capability) < 32:
            raise ValueError('session capability required')
        self.pages = {}
        self.active_pointer_page = None
        self.next_output = 2  # stock pixelflux reserves anchor 0 and its view 1
        self.initialized = False
        self.loop = asyncio.get_running_loop()
        self.frame_queue = asyncio.Queue(maxsize=16)
        self.stream_ready = asyncio.Event()
        self.stats = {'encoded': 0, 'sent': 0, 'dropped': 0, 'gated': 0, 'reconnects': 0}
        self.closed = False
        self.tasks = [asyncio.create_task(self.frames())]

    def emit(self, message):
        self.output.write(json.dumps(message, separators=(',', ':')) + '\n')
        self.output.flush()

    def native(self, op, **args):
        result = json.loads(self.control.pogo_control(json.dumps({'op': op, **args})))
        if isinstance(result, dict) and result.get('error'):
            raise RuntimeError(result['error'])
        return result

    async def init(self, args):
        if self.initialized:
            raise ValueError('already initialized')
        runtime = Path(self.cfg.runtime_dir)
        runtime.mkdir(parents=True, mode=0o700, exist_ok=True)
        if runtime.is_symlink() or runtime.stat().st_uid != os.getuid():
            raise ValueError('runtime directory must be private and owned')
        runtime.chmod(0o700)
        os.environ.update({'XDG_RUNTIME_DIR': str(runtime), 'PIXELFLUX_MANUAL_PLACEMENT': '1', 'WLR_RENDERER': 'pixman'})
        # Native stdout was redirected before this import; protocol keeps its own fd.
        from pixelflux import CaptureSettings, ScreenCapture, ensure_wayland_display
        self.ScreenCapture, self.CaptureSettings = ScreenCapture, CaptureSettings
        self.control = ScreenCapture()
        socket = await asyncio.to_thread(ensure_wayland_display, 640, 480, '', 'false')
        if not socket:
            raise RuntimeError('Wayland compositor initialization failed')
        self.control.set_cursor_rendering(False)
        self.network = args.get('network', 'wan')
        if self.network not in ('lan', 'wan'):
            raise ValueError('invalid network mode')
        self.initialized = True
        return {'wayland_display': socket, 'xdg_runtime_dir': str(runtime), 'scale': args.get('scale', 1.25), 'parking': True, 'codec': 'vp8', 'protocol': 1}

    def page(self, args):
        p = self.pages.get(identifier(args.get('page')))
        if p is None:
            raise ValueError('page is not bound')
        if 'generation' in args and args['generation'] != p['generation']:
            raise ValueError('stale page generation')
        return p

    async def bind(self, args):
        page = identifier(args['page'])
        generation = args.get('generation')
        if isinstance(generation, bool) or not isinstance(generation, int) or generation < 1:
            raise ValueError('generation must be positive')
        window = args['window_id']
        rows = await asyncio.to_thread(self.native, 'windows')
        row = next((w for w in rows if w['window_id'] == window), None)
        if row is None:
            raise ValueError('unknown native window')
        if any(p['window_id'] == window and p['page'] != page for p in self.pages.values()):
            raise ValueError('window already belongs to a Sheet')
        if page in self.pages:
            old = self.pages[page]
            if generation <= old['generation']:
                raise ValueError('generation did not advance')
            await self.unbind({'page': page})
        if not row['parked']:
            raise ValueError('only parked windows may be newly bound')
        w, h, scale, pw, ph = geometry(args)
        display = self.next_output
        self.next_output += 1
        # Slots never overlap even after a resize; input uses physical local CSS*r.
        x = (display - 1) * 8192
        if not self.control.create_output(display, pw, ph, x, 0, scale):
            raise RuntimeError('output allocation failed')
        if not self.control.move_window_to_output(window, display):
            self.control.destroy_output(display)
            raise RuntimeError('native window binding failed')
        p = {'page': page, 'generation': generation, 'window_id': window, 'display_id': display, 'width': w, 'height': h, 'scale': scale, 'pixel_width': pw, 'pixel_height': ph, 'x': x, 'capture': None, 'seq': 0, 'needs_keyframe': True, 'buttons': set(), 'announced': False, 'fenced': True, 'clean_seq': 0, 'encoder_config': None}
        self.pages[page] = p
        return self.meta(p)

    def meta(self, p):
        return {k: p[k] for k in ('page', 'generation', 'window_id', 'display_id', 'width', 'height', 'scale', 'pixel_width', 'pixel_height')} | {'codec': 'vp8', 'profile_level_id': ''}

    async def capture_start(self, args, private=False):
        p = self.pages.get(identifier(args.get('page')))
        if p is None:
            raise ValueError('page is not bound')
        generation = args.get('generation', p['generation'])
        if not isinstance(generation, int) or isinstance(generation, bool) or generation < p['generation']:
            raise ValueError('stale page generation')
        if p['capture'] is not None and generation != p['generation']:
            await self.capture_stop({'page': p['page']})
        p['generation'] = generation
        p['fenced'] = private
        if p['capture'] is not None:
            if not private:
                p['needs_keyframe'] = True
                p['capture'].request_idr_frame()
            return self.meta(p)
        capture = self.ScreenCapture()
        settings = self.CaptureSettings()
        for k, v in {'use_wayland': True, 'use_cpu': True, 'encode_node_index': -1, 'auto_gpu': 'false', 'codec': 'vp8', 'video_fullframe': True, 'omit_stripe_headers': True, 'capture_cursor': False, 'capture_width': p['pixel_width'], 'capture_height': p['pixel_height'], 'scale': p['scale'], 'display_id': p['display_id'], 'auto_adjust_screen_capture_size': False, 'target_fps': 60., 'video_crf': 25, 'video_paintover_crf': 18, 'video_paintover_burst_frames': 5, 'paint_over_trigger_frames': 15, 'use_paint_over_quality': True, 'keyframe_interval_s': 0., 'video_streaming_mode': False, 'video_fullcolor': False, 'debug_logging': False}.items():
            setattr(settings, k, v)
        rate = p.get('encoder_config') or {'bitrate_kbps': 4000, 'fps': 60, 'cbr': self.network == 'wan'}
        settings.video_cbr_mode = rate['cbr']
        settings.video_bitrate_kbps = rate['bitrate_kbps']
        p['capture'] = capture
        source = {k: p[k] for k in ('page', 'display_id', 'generation', 'pixel_width', 'pixel_height')}
        def callback(frame):
            if frame.data_type != VP8_DATA_TYPE or not len(frame) or len(frame) > MAX_PAYLOAD:
                return
            payload = bytes(frame)
            key = payload[0] & 1 == 0
            if key and (len(payload) < 10 or payload[3:6] != b'\x9d\x01\x2a'):
                return
            p['seq'] += 1
            header = {'page': source['page'], 'display_id': source['display_id'], 'generation': source['generation'], 'seq': p['seq'], 'capture_ns': int(frame.capture_ns), 'encode_start_ns': int(frame.encode_start_ns), 'encode_end_ns': int(frame.encode_end_ns), 'content_seq': int(frame.content_seq), 'width': source['pixel_width'], 'height': source['pixel_height'], 'codec': 'vp8', 'keyframe': key, 'frame_id': int(frame.frame_id), 'reference_frame_id': int(frame.reference_frame_id), 'payload_bytes': len(payload), 'profile_level_id': ''}
            private = p['fenced']
            self.loop.call_soon_threadsafe(self.enqueue, p, header, payload, private, capture)
        await asyncio.to_thread(capture.start_capture, callback, settings)
        actual = await asyncio.to_thread(self.control.get_realized_geometry, p['display_id'])
        if actual != (p['pixel_width'], p['pixel_height'], p['scale']) or capture.capture_state(p['display_id'])[0] != 'running':
            await self.capture_stop(args)
            raise RuntimeError('capture geometry or encoder initialization failed')
        return self.meta(p)

    def enqueue(self, p, header, payload, private, owner=None):
        if self.pages.get(p['page']) is not p or p['capture'] is None or (owner is not None and p['capture'] is not owner) or private or header['content_seq'] < p['clean_seq']:
            return
        self.stats['encoded'] += 1
        if not p['announced'] and header['keyframe'] and not p['fenced']:
            p['announced'] = True
            self.emit({'event': 'frame_metadata', **header})
        if p['fenced'] or not self.stream_ready.is_set():
            p['needs_keyframe'] = True
            self.stats['gated'] += 1
            return
        if self.frame_queue.full():
            dropped, _, _ = self.frame_queue.get_nowait()
            dropped['needs_keyframe'] = True
            if dropped['capture']:
                dropped['capture'].request_idr_frame()
            self.stats['dropped'] += 1
        if p['needs_keyframe'] and not header['keyframe']:
            self.stats['gated'] += 1
            p['capture'].request_idr_frame()
            return
        p['needs_keyframe'] = False
        self.frame_queue.put_nowait((p, header, payload))

    async def frames(self):
        while not self.closed:
            writer = None
            try:
                reader, writer = await asyncio.open_unix_connection(self.cfg.frames_socket)
                writer.write((json.dumps({'version': 1, 'session': self.cfg.session, 'token': self.capability}) + '\n').encode())
                await writer.drain()
                self.stream_ready.set()
                for p in self.pages.values():
                    p['needs_keyframe'] = True
                    if p['capture']:
                        p['capture'].request_idr_frame()
                async def controls():
                    while True:
                        raw = await reader.readline()
                        if not raw or len(raw) > MAX_LINE:
                            raise ConnectionError('frame IPC closed')
                        msg = json.loads(raw)
                        if msg.get('type') == 'request_idr' and msg.get('session') == self.cfg.session:
                            p = self.pages.get(msg.get('page'))
                            if p and p['display_id'] == msg.get('display_id') and p['generation'] == msg.get('generation') and p['capture']:
                                p['capture'].request_idr_frame()
                        elif msg.get('type') == 'encoder_config' and msg.get('session') == self.cfg.session:
                            p = self.pages.get(msg.get('page'))
                            budget = msg.get('bitrate_kbps')
                            if p and p['display_id'] == msg.get('display_id') and p['generation'] == msg.get('generation') and isinstance(budget, int) and not isinstance(budget, bool) and 250 <= budget <= 12000 and msg.get('fps') == 60 and msg.get('cbr') is True:
                                p['encoder_config'] = {'bitrate_kbps': budget, 'fps': 60, 'cbr': True}
                                if p['capture']:
                                    p['capture'].update_video_bitrate(budget)
                                    p['capture'].update_framerate(60.)
                task = asyncio.create_task(controls())
                try:
                    while not task.done():
                        packet_task = asyncio.create_task(self.frame_queue.get())
                        done, _ = await asyncio.wait([task, packet_task], return_when=asyncio.FIRST_COMPLETED)
                        if task in done:
                            packet_task.cancel()
                            await task
                        p, header, payload = packet_task.result()
                        if self.pages.get(p['page']) is not p or p['capture'] is None or p['fenced'] or header['content_seq'] < p['clean_seq'] or header['generation'] != p['generation'] or header['width'] != p['pixel_width'] or header['height'] != p['pixel_height']:
                            continue
                        encoded = json.dumps(header, separators=(',', ':')).encode()
                        writer.write(struct.pack('!I', len(encoded)) + encoded + payload)
                        await asyncio.wait_for(writer.drain(), 3)
                        self.stats['sent'] += 1
                finally:
                    task.cancel()
            except (OSError, ValueError, asyncio.TimeoutError, ConnectionError) as e:
                self.stats['reconnects'] += 1
                # Only error class is logged; never capability/page content/SDP.
                print('frame IPC retry: ' + type(e).__name__, file=sys.stderr)
            finally:
                self.stream_ready.clear()
                while not self.frame_queue.empty():
                    self.frame_queue.get_nowait()
                if writer:
                    writer.close()
                    await writer.wait_closed()
            if not self.closed:
                await asyncio.sleep(.25)

    async def capture_stop(self, args):
        p = self.page(args)
        p['fenced'] = True
        keep = []
        while not self.frame_queue.empty():
            packet = self.frame_queue.get_nowait()
            if packet[0] is not p:
                keep.append(packet)
        for packet in keep:
            self.frame_queue.put_nowait(packet)
        capture, p['capture'] = p['capture'], None
        if capture:
            await asyncio.to_thread(capture.stop_capture)
        p['needs_keyframe'] = True
        return {'ack': True}

    async def pointer(self, args):
        p = self.page(args)
        x, y = args.get('x', 0), args.get('y', 0)
        if not all(isinstance(v, (int, float)) and math.isfinite(v) for v in (x, y)) or not (0 <= x < p['width'] and 0 <= y < p['height']):
            raise ValueError('pointer coordinates outside page')
        mods = args.get('modifiers') or {}
        kind = args.get('kind')
        if kind == 'wheel' and mods.get('ctrl'):
            raise ValueError('Ctrl+wheel belongs to CDP input route')
        if self.active_pointer_page is not None and self.active_pointer_page != p['page']:
            old = self.pages.get(self.active_pointer_page)
            if old is not None:
                await self.release({'page': old['page']})
        self.active_pointer_page = p['page']
        await asyncio.to_thread(self.native, 'focus', window_id=p['window_id'])
        await asyncio.to_thread(self.native, 'modifiers', **{k: bool(mods.get(k)) for k in ('ctrl', 'shift', 'alt', 'logo')})
        self.control.inject_mouse_move(p['x'] + x * p['scale'], y * p['scale'])
        target = await asyncio.to_thread(self.native, 'pointer_target')
        if target.get('window_id') != p['window_id'] or target.get('grab_window_id') not in (None, p['window_id']):
            raise RuntimeError('native pointer target does not match the requested Sheet')
        if kind == 'button':
            raw_button = args.get('button')
            button = {'left': 0, 'middle': 1, 'right': 2}.get(raw_button, raw_button)
            if isinstance(button, bool) or button not in (0, 1, 2):
                raise ValueError('unknown mouse button')
            native = {0: 272, 1: 274, 2: 273}[button]
            pressed = args.get('pressed')
            if not isinstance(pressed, bool):
                raise ValueError('button pressed must be boolean')
            self.control.inject_mouse_button(native, int(pressed))
            if pressed:
                p['buttons'].add(native)
            else:
                p['buttons'].discard(native)
        elif kind == 'wheel':
            dx, dy = args.get('dx', 0), args.get('dy', 0)
            if not all(isinstance(v, (int, float)) and math.isfinite(v) and abs(v) <= 5000 for v in (dx, dy)):
                raise ValueError('invalid wheel delta')
            # Stock Wayland v120 units observed by Chromium153: 12 CSS px/unit.
            self.control.inject_mouse_scroll(dx / 12., dy / 12.)
        elif kind != 'move':
            raise ValueError('unknown pointer operation')
        # Same channel: confirms native seat dispatch, not visible-frame latency.
        await asyncio.to_thread(self.native, 'sync', window_id=p['window_id'])
        return {'ack': True}

    async def release(self, args):
        p = self.page(args)
        target = await asyncio.to_thread(self.native, 'pointer_target')
        owns_pointer = p['window_id'] in (target.get('window_id'), target.get('grab_window_id'))
        if self.active_pointer_page == p['page'] and owns_pointer:
            for button in list(p['buttons']):
                self.control.inject_mouse_button(button, 0)
            self.active_pointer_page = None
        p['buttons'].clear()
        await asyncio.to_thread(self.native, 'release', window_id=p['window_id'])
        return {'ack': True}

    async def unbind(self, args):
        p = self.page(args)
        await self.release(args)
        await self.capture_stop(args)
        parked = await asyncio.to_thread(self.native, 'park_if_present', window_id=p['window_id'])
        if not isinstance(parked, dict) or parked.get('ack') is not True or not isinstance(parked.get('present'), bool):
            raise RuntimeError('native window cleanup was not acknowledged')
        if self.control.destroy_output(p['display_id']) is not True:
            raise RuntimeError('output destruction was not acknowledged')
        self.pages.pop(p['page'])
        return {'ack': True}

    async def dispatch(self, op, args):
        if op == 'init':
            return await self.init(args)
        if op == 'shutdown':
            for page in list(self.pages):
                await self.unbind({'page': page})
            self.closed = True
            return {'ack': True}
        if not self.initialized:
            raise ValueError('init required')
        if op == 'windows':
            return await asyncio.to_thread(self.native, 'windows')
        if op == 'bind_sheet':
            return await self.bind(args)
        if op == 'capture_start':
            return await self.capture_start(args)
        if op == 'capture_stop':
            return await self.capture_stop(args)
        if op == 'pointer':
            return await self.pointer(args)
        if op == 'release':
            return await self.release(args)
        if op == 'unbind_sheet':
            return await self.unbind(args)
        if op == 'status':
            return {'codec': 'vp8', 'pages': [self.meta(p) for p in self.pages.values()], 'stats': self.stats, 'ipc_connected': self.stream_ready.is_set()}
        p = self.page(args)
        if op == 'resize':
            w, h, r, pw, ph = geometry(args)
            active = p['capture'] is not None
            await self.capture_stop(args)
            if not self.control.resize_output(p['display_id'], pw, ph, r):
                raise RuntimeError('output resize failed')
            p.update(width=w, height=h, scale=r, pixel_width=pw, pixel_height=ph, announced=False)
            if active:
                await self.capture_start(args)
            return self.meta(p)
        if op == 'sync':
            return await asyncio.to_thread(self.native, 'sync', window_id=p['window_id'])
        if op in ('probe_present', 'probe_absent'):
            if not p['fenced']:
                raise RuntimeError('capture_stop required before private pixel probe')
            if p['capture'] is None:
                await self.capture_start({'page': p['page']}, private=True)
            result = await asyncio.to_thread(self.native, op, output_id=p['display_id'], **{k: args[k] for k in ('nonce', 'pixel_x', 'pixel_y', 'min_seq') if k in args})
            if op == 'probe_absent':
                p['clean_seq'] = result['content_seq']
            return result
        if op == 'request_idr':
            if not p['capture']:
                raise ValueError('capture is stopped')
            p['capture'].request_idr_frame()
            return {'ack': True}
        raise ValueError('unknown host operation')


async def main(cfg, output):
    identifier(cfg.session)
    host = Host(cfg, output)
    try:
        while not host.closed:
            raw = await asyncio.to_thread(sys.stdin.readline, MAX_LINE + 1)
            if not raw:
                break
            if len(raw) > MAX_LINE or not raw.endswith('\n'):
                raise ValueError('control line exceeds limit')
            ident = None
            try:
                msg = json.loads(raw)
                ident = msg.get('id')
                if not isinstance(ident, (str, int)) or isinstance(ident, bool):
                    raise ValueError('RPC id required')
                result = await host.dispatch(msg['op'], msg.get('args') or {})
                host.emit({'id': ident, 'result': result})
            except Exception as e:
                host.emit({'id': ident, 'error': {'code': 'HOST_FAILED', 'message': str(e)}})
    finally:
        if host.initialized:
            for p in list(host.pages):
                try:
                    await host.unbind({'page': p})
                except Exception:
                    pass
        host.closed = True
        for t in host.tasks:
            t.cancel()
        await asyncio.gather(*host.tasks, return_exceptions=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--session', required=True)
    parser.add_argument('--runtime-dir', required=True)
    parser.add_argument('--frames-socket', required=True)
    cfg = parser.parse_args()
    # Rust uses process fd1 directly; a sys.stdout swap alone is insufficient.
    output = os.fdopen(os.dup(1), 'w', buffering=1)
    os.dup2(2, 1)
    asyncio.run(main(cfg, output))
