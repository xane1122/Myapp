package observability

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	activeLogName = "app.log"
	maxLogBytes   = 1 << 20
	maxLogAge     = 72 * time.Hour
)

var (
	eventLogger        = log.New(os.Stdout, "", 0)
	logSink            *rotatingWriter
	beijingLogLocation = time.FixedZone("Asia/Shanghai", 8*60*60)
)

func Init(dir string) error {
	if dir == "" {
		dir = "logs"
	}
	sink, err := newRotatingWriter(dir)
	if err != nil {
		return err
	}
	logSink = sink
	writer := io.MultiWriter(os.Stdout, sink)
	log.SetOutput(writer)
	eventLogger.SetOutput(writer)
	Event("server.logging_ready", map[string]interface{}{
		"path":      filepath.Join(dir, activeLogName),
		"max_bytes": maxLogBytes,
		"max_age":   maxLogAge.String(),
	})
	return nil
}

func Close() error {
	if logSink == nil {
		return nil
	}
	return logSink.Close()
}

func Event(name string, fields map[string]interface{}) {
	if fields == nil {
		fields = map[string]interface{}{}
	}
	fields["ts"] = time.Now().In(beijingLogLocation).Format(time.RFC3339Nano)
	fields["event"] = name
	line, err := json.Marshal(fields)
	if err != nil {
		log.Printf("log marshal failed: %v", err)
		return
	}
	eventLogger.Println(string(line))
}

type rotatingWriter struct {
	mu     sync.Mutex
	dir    string
	path   string
	file   *os.File
	size   int64
	opened time.Time
}

func newRotatingWriter(dir string) (*rotatingWriter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	w := &rotatingWriter{dir: dir, path: filepath.Join(dir, activeLogName)}
	if err := w.open(); err != nil {
		return nil, err
	}
	w.cleanupLocked(time.Now())
	return w, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rotateIfNeededLocked(len(p), time.Now()); err != nil {
		return 0, err
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *rotatingWriter) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file = file
	w.size = info.Size()
	w.opened = info.ModTime()
	if w.opened.IsZero() {
		w.opened = time.Now()
	}
	return nil
}

func (w *rotatingWriter) rotateIfNeededLocked(nextBytes int, now time.Time) error {
	tooLarge := w.size > 0 && w.size+int64(nextBytes) > maxLogBytes
	tooOld := w.size > 0 && now.Sub(w.opened) >= maxLogAge
	if !tooLarge && !tooOld {
		return nil
	}
	if err := w.rotateLocked(now); err != nil {
		return err
	}
	w.cleanupLocked(now)
	return nil
}

func (w *rotatingWriter) rotateLocked(now time.Time) error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	if _, err := os.Stat(w.path); err == nil {
		archive := filepath.Join(w.dir, "app-"+now.Format("20060102-150405")+".log")
		for i := 1; ; i++ {
			if _, err := os.Stat(archive); os.IsNotExist(err) {
				break
			}
			archive = filepath.Join(w.dir, "app-"+now.Format("20060102-150405")+"-"+strconv.Itoa(i)+".log")
		}
		if err := os.Rename(w.path, archive); err != nil {
			return err
		}
	}
	return w.open()
}

func (w *rotatingWriter) cleanupLocked(now time.Time) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "app-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxLogAge {
			_ = os.Remove(filepath.Join(w.dir, name))
		}
	}
}
