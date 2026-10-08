package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A picture is the logo of a service that has no drawing the dashboard can
// show as it is: its sources have a PNG, a JPEG or a WebP, or an SVG with
// text, a link, a script or a picture inside it. It is drawn once, here,
// at twice the size a card shows it, and kept as a WebP file of a few
// kilobytes: logo-<key>.webp, where a drawing is logo-<key>.svg.
//
// A browser does the drawing, since it is what will show the file and it
// reads every kind there is. It is run without a window on a page this
// program serves to it from this machine, and the page sends back what it
// made. A file is shown to it as an image and in no other way: an image
// runs no script and fetches nothing.

// pictureSize is the longer side of a picture: a card shows a logo at
// 48px, a screen of twice the density at 96.
const pictureSize = 96

// wanted is a service without a logo and the files that may be one, the
// likeliest first.
type wanted struct {
	Key   string
	files []logoFile
}

// picture is what the browser made of the first file that will do.
type picture struct {
	Key string `json:"key"`
	// From is that file, as services.SOURCES names it.
	From string `json:"from"`
	// Dark is set for a logo drawn in white, which is on a dark square.
	Dark bool   `json:"dark"`
	WebP []byte `json:"webp"`
	// Notes say why each file before it would not do.
	Notes []string `json:"notes"`
}

var pictureKinds = map[string]string{
	".svg": "image/svg+xml", ".png": "image/png", ".webp": "image/webp", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".ico": "image/x-icon",
}

// findBrowser returns Chrome or Chromium: the one named, or the first
// there is where they are usually installed.
func findBrowser(named string) (string, error) {
	if named != "" {
		return named, nil
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	for _, path := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("the logos that are pictures are drawn by Chrome or Chromium, and neither was found: name one with -chrome")
}

// pictures has the browser make a picture for each service that has a
// file it can make one of. A service with none is not in the answer.
func pictures(browser string, want []wanted) (map[string]picture, error) {
	type file struct {
		From   string `json:"from"`
		URL    string `json:"url"`
		Vector bool   `json:"vector"`
		// ForDark is set for a logo for a dark page that the page would
		// not know for one (logoForDark).
		ForDark bool `json:"forDark"`
	}
	type job struct {
		Key   string `json:"key"`
		Files []file `json:"files"`
	}
	var jobs []job
	paths := map[string]string{}
	for i, w := range want {
		j := job{Key: w.Key}
		seen := map[string]bool{}
		for n, f := range w.files {
			kind := strings.ToLower(filepath.Ext(f.file))
			info, err := os.Stat(f.file)
			// A file of 4 MB is no logo, whatever is in it.
			if pictureKinds[kind] == "" || seen[f.file] || err != nil || info.IsDir() || info.Size() > 4<<20 {
				continue
			}
			seen[f.file] = true
			url := "/file/" + strconv.Itoa(i) + "/" + strconv.Itoa(n) + kind
			paths[url] = f.file
			j.Files = append(j.Files, file{From: f.from, URL: url, Vector: kind == ".svg", ForDark: logoForDark[f.from]})
		}
		if len(j.Files) > 0 {
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 {
		return nil, nil
	}

	done := make(chan []picture, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, picturePage, pictureSize, smallLogo, darkPage)
	})
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jobs)
	})
	mux.HandleFunc("GET /file/", func(w http.ResponseWriter, r *http.Request) {
		path, ok := paths[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", pictureKinds[strings.ToLower(filepath.Ext(path))])
		w.Write(raw)
	})
	mux.HandleFunc("POST /done", func(w http.ResponseWriter, r *http.Request) {
		var made []picture
		if err := json.NewDecoder(r.Body).Decode(&made); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case done <- made:
		default:
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	profile, err := os.MkdirTemp("", "catalog-browser-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(profile)
	// The browser does not end by itself once the page is done: it is
	// ended here, when the page has answered or has taken too long.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var said bytes.Buffer
	cmd := exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--disable-background-networking", "--disable-component-update", "--disable-sync",
		"--user-data-dir="+profile, "http://"+listener.Addr().String()+"/")
	cmd.Stderr = &said
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", browser, err)
	}
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	defer func() {
		cancel()
		<-ended
	}()

	var made []picture
	select {
	case made = <-done:
	case err := <-ended:
		ended <- err
		return nil, fmt.Errorf("%s ended before it had drawn the logos: %v\n%s", browser, err, lastLines(said.String(), 5))
	case <-ctx.Done():
		return nil, fmt.Errorf("%s did not draw the logos in five minutes\n%s", browser, lastLines(said.String(), 5))
	}
	out := map[string]picture{}
	for _, p := range made {
		// What comes back is checked like any file from outside: a WebP
		// file, of the size the page was told.
		if p.From != "" && (len(p.WebP) < 16 || len(p.WebP) > smallLogo || string(p.WebP[:4]) != "RIFF" || string(p.WebP[8:12]) != "WEBP") {
			return nil, fmt.Errorf("%s: the browser's picture of %s is no WebP file of at most %d bytes (%d)", p.Key, p.From, smallLogo, len(p.WebP))
		}
		out[p.Key] = p
	}
	return out, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// picturePage is the page the browser is given. For each service it goes
// through the files in their order and takes the first that is a logo:
//
//   - A picture smaller than a card shows it is none.
//   - What is around the logo is cut off: the transparent edge, or the
//     white paper of a picture that has no transparency. A logo then
//     fills its card like the drawings do.
//   - A logo that is white, as far as it is anything, is kept for last. If
//     no file shows on a white card, the first such one is put on a dark
//     square.
//   - The picture is never made larger than its file, and is written as
//     WebP with as much quality as fits the size.
const picturePage = `<!doctype html>
<meta charset="utf-8">
<title>logos</title>
<script>
const SIZE = %d, LIMIT = %d, DARK = %q, CARD = [246, 248, 250];

function look(img, vector) {
  let nw = img.naturalWidth, nh = img.naturalHeight;
  if (!nw || !nh) {
    if (!vector) return {no: "no size"};
    nw = nh = 512;
  }
  if (!vector && Math.max(nw, nh) < 48) return {no: "smaller than 48px"};
  const longer = Math.max(nw, nh);
  const f = vector || longer > 512 ? 512 / longer : 1;
  const w = Math.max(1, Math.round(nw * f)), h = Math.max(1, Math.round(nh * f));
  const canvas = document.createElement("canvas");
  canvas.width = w; canvas.height = h;
  const ctx = canvas.getContext("2d", {willReadFrequently: true});
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(img, 0, 0, w, h);
  const d = ctx.getImageData(0, 0, w, h).data;
  let solid = 0, seen = 0, other = 0;
  for (let i = 0; i < w * h; i++) {
    const a = d[i * 4 + 3] / 255;
    if (a <= 0.1) continue;
    solid++;
    let off = 0;
    for (let c = 0; c < 3; c++) off += Math.abs(a * d[i * 4 + c] + (1 - a) * CARD[c] - CARD[c]);
    if (off > 60) seen++;
    if (Math.abs(d[i * 4] - d[0]) + Math.abs(d[i * 4 + 1] - d[1]) + Math.abs(d[i * 4 + 2] - d[2]) > 30) other++;
  }
  if (!solid) return {no: "nothing in it"};
  const whole = solid > 0.995 * w * h;
  if (whole && !other) return {no: "one colour all over"};
  // White paper around a logo is cut off like a transparent edge. A ground
  // of any other colour is the logo's own, the square of an app's icon.
  const paper = whole && [0, w - 1, (h - 1) * w, h * w - 1].every(i => d[i * 4] > 235 && d[i * 4 + 1] > 235 && d[i * 4 + 2] > 235);
  let x0 = 0, y0 = 0, x1 = w - 1, y1 = h - 1;
  if (paper || !whole) {
    x0 = w; y0 = h; x1 = -1; y1 = -1;
    for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
      const i = (y * w + x) * 4;
      const part = paper ? d[i] < 225 || d[i + 1] < 225 || d[i + 2] < 225 : d[i + 3] > 25;
      if (!part) continue;
      if (x < x0) x0 = x; if (x > x1) x1 = x; if (y < y0) y0 = y; if (y > y1) y1 = y;
    }
  }
  if (paper) {
    // A little of the paper stays around the logo.
    const pad = Math.round(0.08 * Math.max(x1 - x0, y1 - y0));
    x0 = Math.max(0, x0 - pad); y0 = Math.max(0, y0 - pad); x1 = Math.min(w - 1, x1 + pad); y1 = Math.min(h - 1, y1 + pad);
  }
  return {canvas, x: x0, y: y0, w: x1 - x0 + 1, h: y1 - y0 + 1, vector, white: !whole && seen < 0.12 * solid};
}

function drawn(l, dark) {
  const out = document.createElement("canvas");
  const ctx = out.getContext("2d");
  let room = SIZE, f;
  if (dark) {
    out.width = out.height = SIZE;
    ctx.fillStyle = DARK;
    ctx.beginPath(); ctx.roundRect(0, 0, SIZE, SIZE, SIZE * 10 / 48); ctx.fill();
    room = SIZE * 34 / 48;
    f = Math.min(room / l.w, room / l.h);
  } else {
    f = Math.min(room / l.w, room / l.h, 1);
    out.width = Math.max(1, Math.round(l.w * f)); out.height = Math.max(1, Math.round(l.h * f));
  }
  const w = Math.max(1, Math.round(l.w * f)), h = Math.max(1, Math.round(l.h * f));
  ctx.imageSmoothingQuality = "high";
  // A step at a time: a browser halves no further than once in one go,
  // and a logo taken from 512px to 96 in one is rough at its edges.
  let from = l.canvas, sx = l.x, sy = l.y, sw = l.w, sh = l.h;
  while (sw > 2 * w) {
    const half = document.createElement("canvas");
    half.width = Math.ceil(sw / 2); half.height = Math.ceil(sh / 2);
    const hc = half.getContext("2d");
    hc.imageSmoothingQuality = "high";
    hc.drawImage(from, sx, sy, sw, sh, 0, 0, half.width, half.height);
    from = half; sx = 0; sy = 0; sw = half.width; sh = half.height;
  }
  ctx.drawImage(from, sx, sy, sw, sh, (out.width - w) / 2, (out.height - h) / 2, w, h);
  return out;
}

async function webp(canvas) {
  for (const q of [0.9, 0.82, 0.74, 0.66, 0.58, 0.5, 0.4]) {
    const blob = await new Promise(done => canvas.toBlob(done, "image/webp", q));
    if (blob && blob.type === "image/webp" && blob.size <= LIMIT) {
      const bytes = new Uint8Array(await blob.arrayBuffer());
      let s = "";
      for (let i = 0; i < bytes.length; i += 4096) s += String.fromCharCode(...bytes.subarray(i, i + 4096));
      return btoa(s);
    }
  }
  return "";
}

async function one(job) {
  const notes = [];
  let white = null;
  for (const file of job.files) {
    const img = new Image();
    img.src = file.url;
    try { await img.decode(); } catch (e) { notes.push(file.from + ": no picture a browser reads"); continue; }
    const l = look(img, file.vector);
    if (l.no) { notes.push(file.from + ": " + l.no); continue; }
    if (l.white || file.forDark) { notes.push(file.from + ": white, for a dark page"); white = white || {l, from: file.from}; continue; }
    const made = await webp(drawn(l, false));
    if (!made) { notes.push(file.from + ": larger than " + LIMIT + " bytes as a picture"); continue; }
    return {key: job.key, from: file.from, dark: false, webp: made, notes};
  }
  if (white) {
    const made = await webp(drawn(white.l, true));
    if (made) return {key: job.key, from: white.from, dark: true, webp: made, notes};
  }
  return {key: job.key, from: "", dark: false, webp: "", notes};
}

(async () => {
  const jobs = await (await fetch("/jobs")).json();
  const made = [];
  for (const job of jobs) made.push(await one(job));
  await fetch("/done", {method: "POST", body: JSON.stringify(made)});
})();
</script>
`
