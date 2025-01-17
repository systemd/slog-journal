package slogjournal

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/slogtest"
	"time"
)

// Deserialize serialized data into key-value pairs
func deserializeKeyValue(r io.Reader) (map[string]string, error) {
	kvPairs := make(map[string]string)
	buf := make([]byte, 1024)
	for {
		key, err := readUntil(r, []byte{'=', '\n'}, buf)
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}

		if key[len(key)-1] == '=' {
			// First method
			key = key[:len(key)-1]
			value, err := readUntil(r, []byte{'\n'}, buf)
			if err != nil {
				return nil, err
			}
			value = value[:len(value)-1] // Remove the trailing newline
			kvPairs[string(key)] = string(value)
		} else {
			// Second method
			key = key[:len(key)-1]
			var valueLen uint64
			if err := binary.Read(r, binary.LittleEndian, &valueLen); err != nil {
				return nil, err
			}
			value := make([]byte, valueLen)
			if _, err := io.ReadFull(r, value); err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(r, buf[:1]); err != nil {
				return nil, err
			}
			kvPairs[string(key)] = string(value)
		}
	}

	return kvPairs, nil
}

// Helper function to read until one of the delimiter bytes is encountered
func readUntil(r io.Reader, delimiters []byte, buf []byte) ([]byte, error) {
	var result bytes.Buffer
	for {
		n, err := r.Read(buf[:1])
		if n > 0 {
			result.WriteByte(buf[0])
			for _, delimiter := range delimiters {
				if buf[0] == delimiter {
					return result.Bytes(), nil
				}
			}
		}
		if err != nil {
			if err == io.EOF && result.Len() > 0 {
				return result.Bytes(), nil
			}
			return nil, err
		}
	}
}

func TestBasicFunctionality(t *testing.T) {
	buf := new(bytes.Buffer)
	handler, err := NewHandler(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.w = buf
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "Hello, World!", 0)
	record.AddAttrs(slog.Attr{Key: "key", Value: slog.StringValue("value")})

	_ = handler.Handle(context.TODO(), record)
	kv, err := deserializeKeyValue(buf)
	if err != nil {
		t.Fatal(err)
	}
	if kv["MESSAGE"] != "Hello, World!" {
		t.Error("Unexpected message")
	}
	if kv["PRIORITY"] != "6" {
		t.Error("Unexpected priority", kv)
	}
	if kv["key"] != "value" {
		t.Error("Unexpected attribute", kv)
	}

	recordNoTime := slog.NewRecord(time.Time{}, slog.LevelInfo, "Hello, World!", 0)

	_ = handler.Handle(context.TODO(), recordNoTime)
	kv, err = deserializeKeyValue(buf)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := kv["SYSLOG_TIMESTAMP"]
	if ok {
		t.Error("Unexpected timestamp", v, kv)
	}

}

func TestWithAttrs(t *testing.T) {
	h, err := NewHandler(nil)
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	h.w = buf

	h2 := h.WithAttrs([]slog.Attr{{Key: "KEY2", Value: slog.StringValue("value2")}})
	h3 := h2.WithAttrs([]slog.Attr{{Key: "KEY3", Value: slog.StringValue("value3")}})

	_ = h2.Handle(context.TODO(), slog.NewRecord(time.Now(), slog.LevelInfo, "Hello, World!", 0))

	kv, err := deserializeKeyValue(buf)
	if err != nil {
		t.Fatal(err)
	}
	if kv["KEY2"] != "value2" {
		t.Error("expected KEY2=value2", kv)
	}
	_ = h3.Handle(context.TODO(), slog.NewRecord(time.Now(), slog.LevelInfo, "Hello, World!", 0))
	kv, err = deserializeKeyValue(buf)
	if err != nil {
		t.Fatal(err)
	}
	if kv["KEY2"] != "value2" {
		t.Error("expected key2=value2", kv)
	}
	if kv["KEY3"] != "value3" {
		t.Error("expected key3=value3", kv)
	}

}

func TestReplaceAttr(t *testing.T) {
	buf := new(bytes.Buffer)
	handler, err := NewHandler(&Options{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		a.Key = strings.ToUpper(a.Key)
		return a
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler.w = buf
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "Hello, World!", 0)
	record.AddAttrs(slog.Attr{Key: "key", Value: slog.StringValue("value")})

	_ = handler.Handle(context.TODO(), record)
	kv, err := deserializeKeyValue(buf)
	if err != nil {
		t.Fatal(err)
	}
	if kv["KEY"] != "value" {
		t.Error("Unexpected attribute", kv)
	}
}

func TestReplaceGroup(t *testing.T) {
	buf := new(bytes.Buffer)
	handler, err := NewHandler(&Options{
		ReplaceGroup: func(group string) string {
			return strings.ToUpper(group)
		},
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			a.Key = strings.ToUpper(a.Key)
			return a
		}})

	if err != nil {
		t.Fatal(err)
	}
	handler.w = buf
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "Hello, World!", 0)
	record.AddAttrs(slog.Group("group", slog.Attr{Key: "key", Value: slog.StringValue("value")}))

	_ = handler.Handle(context.TODO(), record)
	kv, err := deserializeKeyValue(buf)
	if err != nil {
		t.Fatal(err)
	}
	if kv["GROUP_KEY"] != "value" {
		t.Error("Unexpected attribute", kv)
	}
}

func createNestedMap(m map[string]any, keys []string, value any) {
	for i, key := range keys {
		if i == len(keys)-1 {
			m[key] = value
		} else {
			if _, ok := m[key]; !ok {
				m[key] = make(map[string]any)
			}
			m = m[key].(map[string]any)
		}
	}
}

func TestSlogtest(t *testing.T) {
	var buf bytes.Buffer

	slogtest.Run(t, func(t *testing.T) slog.Handler {
		handler, err := NewHandler(nil)
		if err != nil {
			t.Fatal(err)
		}
		handler.w = &buf
		return handler
	}, func(t *testing.T) map[string]any {
		m := make(map[string]any)
		kv, err := deserializeKeyValue(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range kv {

			groups := strings.Split(k, "_")

			// Put this field nested into the map based on the group
			createNestedMap(m, groups, v)

			switch k {
			case "MESSAGE":
				k = slog.MessageKey
			case "PRIORITY":
				k = slog.LevelKey
			case "SYSLOG_TIMESTAMP":
				k = slog.TimeKey
			}
			m[k] = v
		}
		buf.Reset()
		return m
	})
}

func TestCanWriteMessageToSocket(t *testing.T) {
	tempDir, err := os.MkdirTemp(os.TempDir(), "journal")
	if err != nil {
		t.Fatal(err)
	}
	addr := tempDir + "/socket"
	raddr, err := net.ResolveUnixAddr("unixgram", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUnixgram("unixgram", raddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	handler, err := NewHandler(nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.w.(*journalWriter).addr = raddr

	t.Run("NormalSize", func(t *testing.T) {
		if err := handler.Handle(context.TODO(), slog.Record{Level: slog.LevelInfo, Message: "Hello, World!"}); err != nil {
			t.Fatal(err)
		}

		buf := make([]byte, 1024)
		oob := make([]byte, 1024)

		n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Error("no data read")
		}
		if oobn != 0 {
			t.Error("did not expect oob data")
		}
	})

	t.Run("TooLarge", func(t *testing.T) {

		_ = handler.w.(*journalWriter).conn.SetWriteBuffer(1024)

		largeLog := "Hello, World!"
		for range 1024 {
			largeLog += "a"
		}

		if err := handler.Handle(context.TODO(), slog.Record{Level: slog.LevelInfo, Message: largeLog}); err != nil {
			t.Fatal(err)
		}

		buf := make([]byte, 1024)
		oob := make([]byte, 1024)

		_, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
		if err != nil {
			t.Error(err)
		}

		ctrl, err := syscall.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			t.Error(err)
		}

		for _, m := range ctrl {
			rights, err := syscall.ParseUnixRights(&m)
			if err != nil {
				t.Error(err)
			}
			for _, fd := range rights {
				_ = syscall.SetNonblock(int(fd), true)
				f := os.NewFile(uintptr(fd), "journal")
				defer f.Close()
				_, _ = f.Seek(0, 0)
				buf := make([]byte, 4096)
				n, err := f.Read(buf)
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					t.Error("no data read")
				}
			}
		}

	})

}

func TestLevel(t *testing.T) {
	l := LevelVar{}
	if l.Level() != slog.LevelInfo {
		t.Error("expected LevelInfo")
	}

	h, err := NewHandler(nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.opts.Level.Level() != slog.LevelInfo {
		t.Error("expected LevelInfo")
	}

	l = LevelVar{}
	os.Setenv("DEBUG_INVOCATION", "1")
	if l.Level() != slog.LevelDebug {
		t.Error("expected LevelDebug")
	}

	h, err = NewHandler(nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.opts.Level.Level() != slog.LevelDebug {
		t.Error("expected LevelDebug")
	}

	h, err = NewHandler(&Options{Level: slog.LevelError})
	if err != nil {
		t.Fatal(err)
	}
	if h.opts.Level.Level() != slog.LevelError {
		t.Error("expected LevelError")
	}

	if !h.Enabled(context.TODO(), slog.LevelError) {
		t.Error("expected true")
	}

}
