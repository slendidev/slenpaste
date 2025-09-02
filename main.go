package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"golang.org/x/time/rate"
	"io"
	"math/rand"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	domain       string
	listenAddr   string
	staticDir    string
	expireDur    time.Duration
	expireOnView bool
	limiters     = make(map[string]*rate.Limiter)
	limMu        sync.Mutex
	useHTTPS     bool
)

//go:embed android-chrome-192x192.png android-chrome-512x512.png apple-touch-icon.png favicon-16x16.png favicon-32x32.png favicon.ico site.webmanifest
var assetsFS embed.FS

type meta struct {
	Expiry       time.Time `json:"expiry"`
	ExpireOnView bool      `json:"expire_on_view"`
}

func init() {
	// Ensure correct types for webmanifest on some systems
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

func getLimiter(ip string) *rate.Limiter {
	limMu.Lock()
	defer limMu.Unlock()
	lim, ok := limiters[ip]
	if !ok {
		lim = rate.NewLimiter(rate.Every(5*time.Second), 1)
		limiters[ip] = lim
	}
	return lim
}

func rateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if i := strings.LastIndex(ip, ":"); i != -1 {
			ip = ip[:i]
		}
		lim := getLimiter(ip)
		if !lim.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func randomID(n int) string {
	letters := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/html; charset=utf-8")
	scheme := "http"
	if useHTTPS {
		scheme = "https"
	}
	d := fmt.Sprintf("%s://%s", scheme, domain)

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">

	<title>slenpaste</title>

	<!-- Icons / PWA -->
	<link rel="apple-touch-icon" href="/apple-touch-icon.png">
	<link rel="icon" type="image/png" sizes="32x32" href="/favicon-32x32.png">
	<link rel="icon" type="image/png" sizes="16x16" href="/favicon-16x16.png">
	<link rel="icon" href="/favicon.ico">
	<link rel="manifest" href="/site.webmanifest">
	<meta name="theme-color" content="#ffffff">

	<style>
		body { font-family: system-ui, sans-serif; max-width: 800px; margin: 2rem auto; padding: 0 1rem; }
		pre { background: #f6f6f6; padding: .75rem 1rem; border-radius: .5rem; overflow: auto; }
		fieldset { border: 1px solid #ddd; border-radius: .5rem; padding: .5rem .75rem; }
		#dropzone {
			border: 2px dashed #bbb; border-radius: .75rem; padding: 1.25rem; text-align: center;
			user-select: none;
		}
		#dropzone.dragover { border-color: #333; background: #fafafa; }
		#result { margin-top: .75rem; }
		#result a { font-weight: 600; }
		.controls { display: flex; gap: .5rem; align-items: center; flex-wrap: wrap; }
		.inline { display: inline-flex; gap: .5rem; align-items: center; }
		textarea { width: 100%%; height: 180px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
		.small { color: #666; font-size: .9rem; }
	</style>
</head>
<body>
	<h1>slenpaste</h1>
	<pre>Welcome!

Upload a file:
  curl -F 'file=@yourfile.txt' -F 'expiry=1h' %s/

Upload from stdin (no file param, expire after 5m):
  curl --data-binary @- %s/?expiry=5m < yourfile.txt

Upload from stdin and expire on first view:
  cat yourfile.txt | curl --data-binary @- "%s/?expiry=view"
</pre>

	<form id="form" enctype="multipart/form-data" method="post">
		<input type="file" name="file" id="fileInput">

		<div class="controls" style="margin-top: .75rem">
			<fieldset>
				<legend>Expiry</legend>
				<label><input type="radio" name="expiry" value="0" checked> Never</label>
				<label><input type="radio" name="expiry" value="5m"> 5 minutes</label>
				<label><input type="radio" name="expiry" value="1h"> 1 hour</label>
				<label><input type="radio" name="expiry" value="24h"> 1 day</label>
				<label><input type="radio" name="expiry" value="view"> Expire on first view</label>
			</fieldset>

			<div class="inline">
				<label for="fname">Filename (optional):</label>
				<input type="text" id="fname" placeholder="example.txt">
			</div>

			<input type="submit" value="Upload (fallback)">
		</div>
	</form>

	<h2 style="margin-top:1.5rem">Quick text</h2>
	<p class="small">Type or paste text and upload as a .txt (no file chooser needed).</p>
	<textarea id="textArea" placeholder="Type or paste here..."></textarea>
	<div class="controls" style="margin-top:.5rem">
		<button id="uploadTextBtn" type="button">Upload text</button>
		<label class="inline small"><input type="checkbox" id="textAsMd"> Save as .md</label>
	</div>

	<h2 style="margin-top:1.5rem">Paste or drop images</h2>
	<div id="dropzone" tabindex="0">
		Paste an image (Ctrl/Cmd+V) or drag &amp; drop files here
		<div class="small">Images are uploaded as files. Other files dropped here work too.</div>
	</div>

	<div id="result"></div>

	<script>
	(function() {
		"use strict";

		const form = document.getElementById("form");
		const fileInput = document.getElementById("fileInput");
		const fname = document.getElementById("fname");
		const textArea = document.getElementById("textArea");
		const uploadTextBtn = document.getElementById("uploadTextBtn");
		const textAsMd = document.getElementById("textAsMd");
		const dropzone = document.getElementById("dropzone");
		const result = document.getElementById("result");

		function getExpiry() {
			const checked = form.querySelector('input[name="expiry"]:checked');
			return checked ? checked.value : "0";
		}

		function setBusy(b) {
			if (b) {
				result.textContent = "Uploading...";
			}
		}

		function showLink(url) {
			result.innerHTML = 'URL: <a href="' + url + '" target="_blank" rel="noopener">' + url + '</a>';
			try {
				navigator.clipboard.writeText(url);
				result.innerHTML += ' <span class="small">(copied)</span>';
			} catch(e) {}
		}

		async function uploadFormData(fd, expiry) {
			setBusy(true);
			const res = await fetch("/?expiry=" + encodeURIComponent(expiry), {
				method: "POST",
				body: fd
			});
			if (!res.ok) {
				const t = await res.text();
				result.textContent = "Error: " + t;
				return;
			}
			const url = (await res.text()).trim();
			showLink(url);
		}

		async function uploadBlobAsFile(blob, filename, expiry) {
			const fd = new FormData();
			const file = new File([blob], filename, { type: blob.type || "application/octet-stream" });
			fd.append("file", file);
			return uploadFormData(fd, expiry);
		}

		// Enhance file input submit (without relying on default form submit)
		form.addEventListener("submit", async (e) => {
			// Use JS path when possible; the default submit still works if JS fails.
			e.preventDefault();
			if (!fileInput.files || fileInput.files.length === 0) {
				result.textContent = "Choose a file first.";
				return;
			}
			const expiry = getExpiry();

			// If user provided a filename, recreate the File with that name.
			let file = fileInput.files[0];
			if (fname.value.trim()) {
				file = new File([file], fname.value.trim(), { type: file.type });
			}

			const fd = new FormData();
			fd.append("file", file);
			await uploadFormData(fd, expiry);
			fileInput.value = "";
			fname.value = "";
		});

		// Upload text as a .txt (or .md)
		uploadTextBtn.addEventListener("click", async () => {
			const text = textArea.value;
			if (!text) {
				result.textContent = "Nothing to upload.";
				return;
			}
			const expiry = getExpiry();
			const name = (fname.value.trim() || (textAsMd.checked ? "text.md" : "text.txt"));
			await uploadBlobAsFile(new Blob([text], { type: "text/plain" }), name, expiry);
			// clear only the filename; keep text in case they want to tweak
			fname.value = "";
		});

		// Drag & drop support
		;["dragenter","dragover"].forEach(ev => {
			dropzone.addEventListener(ev, (e) => {
				e.preventDefault(); e.stopPropagation();
				dropzone.classList.add("dragover");
			});
		});
		;["dragleave","drop"].forEach(ev => {
			dropzone.addEventListener(ev, (e) => {
				e.preventDefault(); e.stopPropagation();
				dropzone.classList.remove("dragover");
			});
		});
		dropzone.addEventListener("drop", async (e) => {
			const expiry = getExpiry();
			const files = Array.from(e.dataTransfer.files || []);
			if (files.length === 0) return;
			const fd = new FormData();
			// support multi-file; server will handle each request, so send one by one for URLs
			for (const f of files) {
				fd.set("file", f);
				await uploadFormData(fd, expiry);
			}
		});

		// Paste image (or any file) support
		window.addEventListener("paste", async (e) => {
			const items = e.clipboardData && e.clipboardData.items ? Array.from(e.clipboardData.items) : [];
			if (!items.length) return;
			const expiry = getExpiry();
			let handled = false;
			for (const it of items) {
				if (it.kind === "file") {
					const blob = it.getAsFile();
					if (!blob) continue;
					const ext = (blob.type && blob.type.startsWith("image/")) ? blob.type.split("/")[1] : "bin";
					const name = fname.value.trim() || ("pasted." + ext);
					await uploadBlobAsFile(blob, name, expiry);
					handled = true;
				} else if (it.kind === "string") {
					// If user copies text, allow quick text upload via paste when textarea is focused
					// We'll let browser put text into the textarea naturally; no upload here.
				}
			}
			if (handled) e.preventDefault();
		});
	})();
	</script>
</body>
</html>`, d, d, d)
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	var reader io.Reader
	var ext string

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err == nil {
			if file, header, err := r.FormFile("file"); err == nil {
				defer file.Close()
				reader = file
				ext = filepath.Ext(header.Filename)
			}
		}
	}

	if reader == nil {
		reader = r.Body
		defer r.Body.Close()
	}

	if ext == "" {
		ext = ".txt"
	}

	id := randomID(6)
	filename := id + ext

	if err := os.MkdirAll(staticDir, 0755); err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}
	path := filepath.Join(staticDir, filename)

	out, err := os.Create(path)
	if err != nil {
		http.Error(w, "Save error", http.StatusInternalServerError)
		return
	}
	defer out.Close()

	n, err := io.Copy(out, reader)
	if err != nil {
		http.Error(w, "Write error", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		_ = os.Remove(path)
		http.Error(w, "Empty upload", http.StatusBadRequest)
		return
	}

	expVal := r.URL.Query().Get("expiry")
	var m meta
	switch expVal {
	case "view":
		m.ExpireOnView = true
	case "0":
		// no expiry
	default:
		if d, err := time.ParseDuration(expVal); err == nil {
			m.Expiry = time.Now().Add(d)
		}
	}
	if !m.Expiry.IsZero() || m.ExpireOnView {
		metaBytes, _ := json.Marshal(m)
		_ = os.WriteFile(path+".json", metaBytes, 0644)
	}

	scheme := "http"
	if useHTTPS {
		scheme = "https"
	}
	fmt.Fprintf(w, "%s://%s/%s\n", scheme, domain, filename)
}

func viewHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/")
	if id == "" {
		indexHandler(w, r)
		return
	}
	path := filepath.Join(staticDir, id)
	metaPath := path + ".json"

	if data, err := os.ReadFile(metaPath); err == nil {
		var m meta
		if err := json.Unmarshal(data, &m); err == nil {
			if !m.Expiry.IsZero() && time.Now().After(m.Expiry) {
				_ = os.Remove(path)
				_ = os.Remove(metaPath)
				http.NotFound(w, r)
				return
			}
			if m.ExpireOnView {
				defer os.Remove(path)
				defer os.Remove(metaPath)
			}
		}
	}

	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	ext := filepath.Ext(id)
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mimeType)

	_, _ = io.Copy(w, f)
}

func serveEmbedded(embeddedName, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := assetsFS.ReadFile(embeddedName)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if contentType == "" {
			if ct := mime.TypeByExtension(filepath.Ext(embeddedName)); ct != "" {
				contentType = ct
			}
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		http.ServeContent(w, r, embeddedName, time.Time{}, strings.NewReader(string(b)))
	}
}

func main() {
	flag.StringVar(&domain, "domain", "localhost:8080", "domain name for URLs")
	flag.StringVar(&listenAddr, "listen", "0.0.0.0:8080", "listen address")
	flag.StringVar(&staticDir, "static", "static", "directory to save pastes")
	flag.DurationVar(&expireDur, "expire", 0, "time after which paste expires (e.g. 5m, 1h)")
	flag.BoolVar(&expireOnView, "expire-on-view", false, "delete paste after it's viewed once")
	flag.BoolVar(&useHTTPS, "https", false, "use https:// in generated URLs")
	flag.Parse()

	// Uploads + index
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			rateLimitMiddleware(uploadHandler)(w, r)
		} else {
			viewHandler(w, r)
		}
	})

	// Embedded favicon/PWA assets
	http.HandleFunc("/favicon.ico", serveEmbedded("favicon.ico", "image/x-icon"))
	http.HandleFunc("/apple-touch-icon.png", serveEmbedded("apple-touch-icon.png", "image/png"))
	http.HandleFunc("/favicon-16x16.png", serveEmbedded("favicon-16x16.png", "image/png"))
	http.HandleFunc("/favicon-32x32.png", serveEmbedded("favicon-32x32.png", "image/png"))
	http.HandleFunc("/android-chrome-192x192.png", serveEmbedded("android-chrome-192x192.png", "image/png"))
	http.HandleFunc("/android-chrome-512x512.png", serveEmbedded("android-chrome-512x512.png", "image/png"))
	http.HandleFunc("/site.webmanifest", serveEmbedded("site.webmanifest", "application/manifest+json"))

	fmt.Printf("slenpaste running at http://%s, storing in %s\n", listenAddr, staticDir)
	_ = http.ListenAndServe(listenAddr, nil)
}
