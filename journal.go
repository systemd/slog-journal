package slogjournal

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
)

type Priority int

const (
	priEmerg Priority = iota
	priAlert
	priCrit
	priErr
	priWarning
	priNotice
	priInfo
	priDebug
)

const (
	LevelNotice    slog.Level = 1
	LevelCritical  slog.Level = slog.LevelError + 1
	LevelAlert     slog.Level = slog.LevelError + 2
	LevelEmergency slog.Level = slog.LevelError + 3
)

func levelToPriority(l slog.Level) Priority {
	switch l {
	case slog.LevelDebug:
		return priDebug
	case slog.LevelInfo:
		return priInfo
	case LevelNotice:
		return priNotice
	case slog.LevelWarn:
		return priWarning
	case slog.LevelError:
		return priErr
	case LevelCritical:
		return priCrit
	case LevelAlert:
		return priAlert
	default:
		panic("unreachable")
	}
}

type Options struct {
	Level       slog.Leveler
	ReplaceAttr func(groups []string, a slog.Attr) slog.Attr
}

type Handler struct {
	opts Options
	// NOTE: We only do single Write() calls. Either the message fits in a
	// single datagram, or we send a file descriptor pointing to a tempfd. This
	// makes writes atomic and thus we do not need any additional
	// synchronization.
	w            io.Writer
	prefix       string
	preformatted *bytes.Buffer
}

const sndBufSize = 8 * 1024 * 1024

func NewHandler(opts *Options) (*Handler, error) {
	h := &Handler{}

	if opts != nil {
		h.opts = *opts
	}

	if h.opts.Level == nil {
		// TODO: Implement a leveler that checks DEBUG_INVOCATION=1
		h.opts.Level = slog.LevelInfo
	}

	w, err := newJournalWriter()
	if err != nil {
		return nil, err
	}

	h.w = w
	h.preformatted = new(bytes.Buffer)
	h.prefix = ""

	return h, nil

}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	h.appendKV(buf, "MESSAGE", []byte(r.Message))
	h.appendKV(buf, "PRIORITY", []byte(strconv.Itoa(int(levelToPriority(r.Level)))))
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		h.appendKV(buf, "CODE_FILE", []byte(f.File))
		h.appendKV(buf, "CODE_FUNC", []byte(f.Function))
		h.appendKV(buf, "CODE_LINE", []byte(strconv.Itoa(f.Line)))
	}

	if !r.Time.IsZero() {
		h.appendKV(buf, "TIMESTAMP", []byte(strconv.Itoa(int(r.Time.Unix()))))
	}

	if _, err := buf.ReadFrom(h.preformatted); err != nil {
		return err
	}

	r.Attrs(func(a slog.Attr) bool {
		h.appendAttr(buf, h.prefix, a)
		return true
	})

	_, err := h.w.Write(buf.Bytes())
	return err

}

func (h *Handler) appendKV(b *bytes.Buffer, k string, v []byte) {
	if bytes.IndexByte(v, '\n') != -1 {
		_, _ = b.WriteString(k)
		_ = b.WriteByte('\n')
		_ = binary.Write(b, binary.LittleEndian, uint64(len(v)))
		_, _ = b.Write(v)
	} else {
		_, _ = b.WriteString(k)
		_ = b.WriteByte('=')
		_, _ = b.Write(v)
		_ = b.WriteByte('\n')
	}
}

func (h *Handler) appendAttr(b *bytes.Buffer, prefix string, a slog.Attr) {
	if rep := h.opts.ReplaceAttr; rep != nil && a.Value.Kind() != slog.KindGroup {
		var gs []string
		if h.prefix != "" {
			gs = strings.Split(h.prefix, "_")
		}
		a = rep(gs, a)
	}
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			prefix += a.Key + "_"
		}
		for _, g := range a.Value.Group() {
			h.appendAttr(b, prefix, g)
		}
	} else if key := a.Key; key != "" {
		h.appendKV(b, prefix+key, []byte(a.Value.String()))
	}
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(h.preformatted)
	for _, a := range attrs {
		h.appendAttr(buf, h.prefix, a)
	}
	return &Handler{
		opts:         h.opts,
		w:            h.w,
		prefix:       h.prefix,
		preformatted: buf,
	}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		opts:         h.opts,
		w:            h.w,
		prefix:       h.prefix + name + "_",
		preformatted: h.preformatted,
	}
}

var _ slog.Handler = &Handler{}
