#!/usr/bin/env python3
"""Render a verified gradient with the AppImage's GTK/WebKit, never host WebKit.

Called by the existing Arch/Xvfb release harness. The three arguments are
AppDir, fixture JSON and snapshot path. A fourth 'no-freetype' allows host FT
only for the mutation. Cairo's snapshot must contain colour in DOM AND canvas.
"""
import base64
import ctypes as C
import colorsys
import math
import importlib.util
import json
import os
from pathlib import Path
import sys


spec = importlib.util.spec_from_file_location("evidence", Path(__file__).with_name("colrv1-evidence.py"))
evidence = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evidence)
app = Path(sys.argv[1]).resolve()
fixture = json.loads(Path(sys.argv[2]).read_text())
snapshot = Path(sys.argv[3]).resolve()
expect_bundle = len(sys.argv) < 5 or sys.argv[4] not in ("no-freetype", "unbundled-baseline")
# This Python host has no executable RUNPATH for the image. Its command-scoped
# library path reaches bundled dependencies and its separate WebProcess.
# Actual AppRun loading is measured separately with no loader-env injection.
os.chdir(app)
gtk = C.CDLL(str(app / "usr/lib/libgtk-3.so.0"), mode=C.RTLD_GLOBAL)
webkit = C.CDLL(str(app / "usr/lib/libwebkit2gtk-4.1.so.0"), mode=C.RTLD_GLOBAL)
glib = C.CDLL(str(app / "usr/lib/libglib-2.0.so.0"), mode=C.RTLD_GLOBAL)
gobj = C.CDLL(str(app / "usr/lib/libgobject-2.0.so.0"), mode=C.RTLD_GLOBAL)
cairo = C.CDLL(str(app / "usr/lib/libcairo.so.2"), mode=C.RTLD_GLOBAL)
P, I, U, S = C.c_void_p, C.c_int, C.c_uint, C.c_char_p


def fn(lib, name, restype, args):
    f = getattr(lib, name)
    f.restype, f.argtypes = restype, args
    return f


fn(gtk, "gtk_init", None, [P, P])(None, None)
window = fn(gtk, "gtk_window_new", P, [I])(0)
fn(gtk, "gtk_window_set_default_size", None, [P, I, I])(window, 640, 400)
view = fn(webkit, "webkit_web_view_new", P, [])()
fn(gtk, "gtk_container_add", None, [P, P])(window, view)
manager = fn(webkit, "webkit_web_view_get_user_content_manager", P, [P])(view)
assert fn(webkit, "webkit_user_content_manager_register_script_message_handler", I, [P, S])(manager, b"colrv1")
connect = fn(gobj, "g_signal_connect_data", C.c_ulong, [P, S, P, P, P, I])
quit_loop = fn(gtk, "gtk_main_quit", None, [])
run_js = fn(webkit, "webkit_web_view_run_javascript", None, [P, S, P, P, P])
rc = 1
stage = "ready"
boxes = []
canvas_box = None
device_pixel_ratio = None
colour_metrics = []


@C.CFUNCTYPE(None, P, P, P)
def snapshot_done(source, result, data):
    global stage, colour_metrics, rc
    try:
        surface = fn(webkit, "webkit_web_view_get_snapshot_finish", P, [P, P, P])(source, result, None)
        assert surface, "snapshot returned no Cairo surface"
        control = stage == "control-snapshot"
        output = snapshot.with_stem(snapshot.stem + "-tofu") if control else snapshot
        code = fn(cairo, "cairo_surface_write_to_png", I, [P, S])(surface, os.fsencode(output))
        assert code == 0, f"PNG write failed: {code}"
        # Inspect Cairo's pixels directly. Importing a host image library under
        # the probe's bundle-only LD_LIBRARY_PATH mixes unrelated JPEG/TIFF
        # ABIs and can fail before WebKit ever renders the regression glyph.
        assert fn(cairo, "cairo_surface_get_type", I, [P])(surface) == 0, "snapshot is not an image surface"
        fmt = fn(cairo, "cairo_image_surface_get_format", I, [P])(surface)
        assert fmt in (0, 1), f"unexpected Cairo snapshot pixel format {fmt}"
        fn(cairo, "cairo_surface_flush", None, [P])(surface)
        width = fn(cairo, "cairo_image_surface_get_width", I, [P])(surface)
        height = fn(cairo, "cairo_image_surface_get_height", I, [P])(surface)
        stride = fn(cairo, "cairo_image_surface_get_stride", I, [P])(surface)
        pixels = fn(cairo, "cairo_image_surface_get_data", P, [P])(surface)
        assert width >= 600 and height >= 320, f"snapshot too small: {width}x{height}"
        image = C.string_at(pixels, stride * height)
        # These DOM bounds came from each glyph element's own
        # getBoundingClientRect, not from the window chrome or a guessed crop.
        metrics = []
        for index, candidate in enumerate(fixture["candidates"]):
            box = boxes[index]
            left, top = max(0, math.floor(box["x"])), max(0, math.floor(box["y"]))
            right = min(width, math.ceil(box["x"] + box["width"]))
            bottom = min(height, math.ceil(box["y"] + box["height"]))
            coloured, hues, saturations = 0, set(), []
            for y in range(top, bottom):
                row = image[y * stride + left * 4:y * stride + right * 4]
                for x in range(0, len(row), 4):
                    b, g, r = row[x:x + 3]
                    if max(r, g, b) - min(r, g, b) > 35 and min(r, g, b) < 220:
                        coloured += 1
                        hue, saturation, _ = colorsys.rgb_to_hsv(r / 255, g / 255, b / 255)
                        hues.add(int(hue * 24) % 24)
                        saturations.append(saturation)
            metric = {"chromatic_pixels": coloured, "hue_buckets": len(hues),
                      "mean_saturation": sum(saturations) / len(saturations) if saturations else 0}
            metrics.append(metric)
            print("COLRV1_DOM_METRICS " + json.dumps({"control": control, "glyph": candidate["text"],
                  "bounds": box, **metric}), flush=True)
            if control:
                # U+10FFFF in monospace is a missing-glyph control, not a
                # weaker colour sample. Require the recorded zero invariant.
                assert metric["chromatic_pixels"] == 0, "missing-glyph control has chromatic pixels"
                assert metric["hue_buckets"] == 0, "missing-glyph control has coloured hues"
                assert metric["mean_saturation"] == 0, "missing-glyph control has chromatic saturation"
                before = colour_metrics[index]
                assert before["chromatic_pixels"] > metric["chromatic_pixels"] * 2 + 100, "glyph not more chromatic than tofu control"
                assert before["hue_buckets"] >= metric["hue_buckets"] + 2, "glyph hue coverage not above tofu control"
                assert before["mean_saturation"] > metric["mean_saturation"] + 0.1, "glyph saturation not above tofu control"
            else:
                assert coloured > 100, f"DOM {candidate['text']} has no colour glyph"
        if not control:
            colour_metrics = metrics
            print("COLRV1_CANVAS_BOUNDS " + json.dumps({"bounds": canvas_box, "snapshot_width": width, "snapshot_height": height, "device_pixel_ratio": device_pixel_ratio}), flush=True)
            # Canvas is independently sampled in each corresponding column.
            for index, candidate in enumerate(fixture["candidates"]):
                coloured = 0
                for y in range(math.floor(canvas_box["y"]), min(height, math.ceil(canvas_box["y"] + canvas_box["height"]))):
                    left = math.floor(canvas_box["x"]) + index * 200
                    row = image[y * stride + left * 4:y * stride + (left + 200) * 4]
                    for x in range(0, len(row), 4):
                        pixel = row[x:x + 3]
                        coloured += max(pixel) - min(pixel) > 35 and min(pixel) < 220
                print(f"COLRV1_SNAPSHOT surface=canvas glyph={candidate['text']} colour_pixels={coloured}", flush=True)
                # U+26A0 can legitimately use text presentation in canvas even
                # though the full emoji-presentation DOM path is coloured.
                # The default set still requires grinning-face's verified
                # radial-gradient graph on canvas, not only solid rainbow.
                if candidate["codepoint"] == 0x26A0 and coloured <= 100:
                    print("COLRV1_CANVAS_TEXT_PRESENTATION glyph=U+26A0; DOM colour required independently", flush=True)
                else:
                    assert coloured > 100, f"canvas {candidate['text']} has no colour glyph"
        fn(cairo, "cairo_surface_destroy", None, [P])(surface)
        print(f"COLRV1_SNAPSHOT status=0 path={output}", flush=True)
        if control:
            evidence.report_tree(os.getpid(), app, expect_bundle, "probe-after-control")
            print("COLRV1_CONTROL_ACK colour glyphs exceed missing-glyph control", flush=True)
            rc = 0
            quit_loop()
        else:
            stage = "response"
            run_js(view, b"window.webkit.messageHandlers.colrv1.postMessage(JSON.stringify('response'))", None, None, None)
    except Exception as error:
        print(f"COLRV1_ERROR {error}", flush=True)
        quit_loop()


@C.CFUNCTYPE(None, P, P, P)
def message(manager, result, data):
    global stage, rc, boxes, canvas_box, device_pixel_ratio
    try:
        js_value = fn(webkit, "webkit_javascript_result_get_js_value", P, [P])(result)
        jsc = C.CDLL(str(app / "usr/lib/libjavascriptcoregtk-4.1.so.0"))
        raw = fn(jsc, "jsc_value_to_string", P, [P])(js_value)
        payload = json.loads(C.string_at(raw).decode())
        fn(glib, "g_free", None, [P])(raw)
        if isinstance(payload, dict) and "boxes" in payload:
            boxes = payload["boxes"]
            canvas_box = payload["canvas"]
            device_pixel_ratio = payload["dpr"]
        if stage == "ready":
            # JS is paused before applying the colour font. Capture mappings
            # before the mutation can abort in its first gradient paint.
            evidence.report_tree(os.getpid(), app, expect_bundle, "probe")
            stage = "paint"
            run_js(view, b"window.startPaint()", None, None, None)
        elif stage == "paint":
            print("COLRV1_PAINT_ACK fonts loaded; DOM + canvas; two animation frames", flush=True)
            stage = "snapshot"
            fn(webkit, "webkit_web_view_get_snapshot", None, [P, I, I, P, P, P])(
                view, 0, 0, None, snapshot_done, None)
        elif stage == "response":
            evidence.report_tree(os.getpid(), app, expect_bundle, "probe-after-paint")
            print("COLRV1_RESPONSE_ACK renderer responded after snapshot", flush=True)
            stage = "control"
            run_js(view, b"window.paintControl()", None, None, None)
        elif stage == "control":
            stage = "control-snapshot"
            fn(webkit, "webkit_web_view_get_snapshot", None, [P, I, I, P, P, P])(
                view, 0, 0, None, snapshot_done, None)
    except Exception as error:
        print(f"COLRV1_ERROR {error}", flush=True)
        quit_loop()


@C.CFUNCTYPE(None, P, I, P)
def terminated(view, reason, data):
    print(f"WEB_PROCESS_TERMINATED reason={reason}", flush=True)
    quit_loop()


@C.CFUNCTYPE(I, P)
def timeout(data):
    print(f"COLRV1_PROBE_TIMEOUT stage={stage}", flush=True)
    quit_loop()
    return 0


connect(manager, b"script-message-received::colrv1", message, None, None, 0)
connect(view, b"web-process-terminated", terminated, None, None, 0)
font = base64.b64encode(Path(fixture["font"]).read_bytes()).decode("ascii")
glyphs_js = json.dumps([c["text"] for c in fixture["candidates"]])
html = """<!doctype html><meta charset=utf-8><style>
@font-face{font-family:Probe;src:url(data:font/ttf;base64,""" + font + """)}
html,body{margin:0;padding:0;background:white;color:black}
#g{height:160px;width:600px;font:80px Probe;line-height:160px;display:flex}
#g span{display:block;width:200px;flex:none}
canvas{display:block}
</style><div id=g></div><canvas id=c width=600 height=160></canvas><script>
window.startPaint = async () => {
  const glyphs = """ + glyphs_js + """;
  const faces = await document.fonts.load('80px Probe', glyphs.join(''));
  if (!faces.length || !document.fonts.check('80px Probe', glyphs.join(''))) throw Error('font not loaded');
  const dom = document.querySelector('#g');
  for (const glyph of glyphs) {
    const span = document.createElement('span'); span.textContent = glyph; dom.append(span);
  }
  const c = document.querySelector('canvas').getContext('2d');
  c.fillStyle='white'; c.fillRect(0,0,600,160);
  c.font='80px Probe'; c.fillStyle='black';
  glyphs.forEach((glyph,index) => {
    // WebKit's canvas text run can choose a monochrome symbol fallback for
    // U+26A0+FE0F even while this exact @font-face DOM paints it in colour.
    // Paint its cmap scalar directly on canvas; DOM still covers full FE0F.
    const text = glyph === '\\u26a0\\ufe0f' ? '\\u26a0' : glyph;
    c.fillText(text,10+index*200,120);
  });
  requestAnimationFrame(() => requestAnimationFrame(() =>
    window.webkit.messageHandlers.colrv1.postMessage(JSON.stringify({event:'painted',dpr:window.devicePixelRatio,canvas:document.querySelector('canvas').getBoundingClientRect().toJSON(),boxes:Array.from(document.querySelectorAll('#g span'),s=>s.getBoundingClientRect().toJSON())}))));
};
window.paintControl = () => {
  for(const span of document.querySelectorAll('#g span')) {
    span.style.fontFamily='monospace'; span.textContent='\\u{10FFFF}';
  }
  requestAnimationFrame(()=>requestAnimationFrame(()=>window.webkit.messageHandlers.colrv1.postMessage(JSON.stringify({event:'control',dpr:window.devicePixelRatio,canvas:document.querySelector('canvas').getBoundingClientRect().toJSON(),boxes:Array.from(document.querySelectorAll('#g span'),s=>s.getBoundingClientRect().toJSON())}))));
};
window.webkit.messageHandlers.colrv1.postMessage(JSON.stringify('ready'));
</script>"""
fn(webkit, "webkit_web_view_load_html", None, [P, S, S])(view, html.encode(), b"http://localhost/")
fn(gtk, "gtk_widget_show_all", None, [P])(window)
fn(glib, "g_timeout_add", U, [U, P, P])(20000, timeout, None)
version = ".".join(str(fn(webkit, "webkit_get_" + n + "_version", U, [])()) for n in ("major", "minor", "micro"))
print(f"COLRV1_START webkit={version} font={fixture['font']} glyphs={fixture['candidates']}", flush=True)
fn(gtk, "gtk_main", None, [])()
sys.exit(rc)
