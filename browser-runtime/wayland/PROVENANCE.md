Pogo embeds the original pixelflux 2.1.0 source at upstream commit
1ebcb0a10a96caeba20e1ad09be47cc018a55238. The upstream MPL-2.0 files and
component license records are retained in vendor/pixelflux. Its pinned Smithay
revision is 5de53056b5cafd453371af2d4e955931ce87ef71. Python package builds use
local version 2.1.0+pogo1 so the patched runtime cannot be confused with stock.

Pogo changes are the managed-window parking policy, callbacks for uncaptured
windows, explicit focus/modifier/client sync controls, and an observed-pixel
nonce fence. content_seq is stamped at compositor render/readback and carried
through the encoder queue, rather than read at the later Python callback.
The software path uses Pixman and FFmpeg/libvpx VP8; H.264 is not a media fallback.
The default upstream GPL x264 feature remains linked in this build and its
license/source must be distributed with a release even when VP8 is selected.

Node owns the session's Chromium/profile, browser-use/CDP, and user/agent
control. host.py receives only session-private Wayland and frame capabilities.
The session capability never enters Chrome's environment or command line.
The host frame UDS is authenticated; every frame binds page/output/generation.
Private visual markers are rendered solely into an IPC-fenced capture, and
content_seq >= observed clean_seq is required before any frame can leave it.
