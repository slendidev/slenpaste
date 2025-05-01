package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	domain       string
	listenAddr   string
	staticDir    string
	expireDur    time.Duration
	expireOnView bool
)

type meta struct {
	Expiry       time.Time `json:"expiry"`
	ExpireOnView bool      `json:"expire_on_view"`
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
	w.Header().Add("Content-Type", "text/html")
	fmt.Fprintf(w, `<html><body><pre>Welcome to slenpaste!

Upload a file:
  curl -F 'file=@yourfile.txt' -F 'expiry=1h' http://%s/

Upload from stdin (no file param, expire after 5m):
  curl --data-binary @- http://%s/?expiry=5m < yourfile.txt

Upload from stdin and expire on first view:
  cat yourfile.txt | curl --data-binary @- "http://%s/?expiry=view"

</pre>
<form enctype="multipart/form-data" method="post">
	<input type="file" name="file">

	<fieldset style="margin-top: 1rem">
		<legend>Expiry:</legend>
		<label><input type="radio" name="expiry" value="0" checked> Never</label>
		<label><input type="radio" name="expiry" value="5m"> 5 minutes</label>
		<label><input type="radio" name="expiry" value="1h"> 1 hour</label>
		<label><input type="radio" name="expiry" value="24h"> 1 day</label>
		<label><input type="radio" name="expiry" value="view"> Expire on first view</label>
	</fieldset><br/>

	<input type="submit" value="Upload">
</form>
</body></html>`, domain, domain, domain)
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	var reader io.Reader
	var ext    string

	// handle multipart file upload
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

	// fallback to body upload
	if reader == nil {
		reader = r.Body
		defer r.Body.Close()
	}

	// default extension
	if ext == "" {
		ext = ".txt"
	}

	// generate ID and filename
	id       := randomID(6)
	filename := id + ext

	// ensure storage dir
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}
	path := filepath.Join(staticDir, filename)

	// save file
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
		os.Remove(path)
		http.Error(w, "Empty upload", http.StatusBadRequest)
		return
	}

	// write metadata if needed
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

	// respond with URL including extension
	fmt.Fprintf(w, "http://%s/%s\n", domain, filename)
}

func viewHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/")
	if id == "" {
		indexHandler(w, r)
		return
	}
	path     := filepath.Join(staticDir, id)
	metaPath := path + ".json"

	// load and enforce metadata
	if data, err := os.ReadFile(metaPath); err == nil {
		var m meta
		if err := json.Unmarshal(data, &m); err == nil {
			if !m.Expiry.IsZero() && time.Now().After(m.Expiry) {
				os.Remove(path)
				os.Remove(metaPath)
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
	w.Header().Set("Content-Type", "text/plain")
	io.Copy(w, f)
}

func main() {
	flag.StringVar(&domain,     "domain",         "localhost:8080", "domain name for URLs")
	flag.StringVar(&listenAddr, "listen",         "0.0.0.0:8080",  "listen address")
	flag.StringVar(&staticDir,  "static",         "static",        "directory to save pastes")
	flag.DurationVar(&expireDur, "expire",         0,                "time after which paste expires (e.g. 5m, 1h)")
	flag.BoolVar(&expireOnView, "expire-on-view", false,            "delete paste after it's viewed once")
	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			uploadHandler(w, r)
		} else {
			viewHandler(w, r)
		}
	})

	fmt.Printf("slenpaste running at http://%s, storing in %s\n", listenAddr, staticDir)
	http.ListenAndServe(listenAddr, nil)
}
