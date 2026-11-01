package main

import (
	"bytes"
	"encoding/json"
	"io"
)

type usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
}

// usageSniffer passes a response body through untouched, as it arrives, and reads the token
// counts out of it on the way: the last "usage" of a stream, or the one in a JSON body. It keeps
// only what it needs to find them and never hands any of it to the journal.
type usageSniffer struct {
	r      io.ReadCloser
	stream bool
	line   []byte // the incomplete line of a stream
	body   []byte // a JSON body, up to maxJSON
	over   bool
	got    usage
	done   bool
}

const (
	maxStreamLine = 1 << 16
	maxJSON       = 4 << 20
)

func newUsageSniffer(r io.ReadCloser, stream bool) *usageSniffer {
	return &usageSniffer{r: r, stream: stream}
}

func (s *usageSniffer) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.see(p[:n])
	}
	if err == io.EOF {
		s.finish()
	}
	return n, err
}

func (s *usageSniffer) Close() error {
	s.finish()
	return s.r.Close()
}

func (s *usageSniffer) see(b []byte) {
	if !s.stream {
		if s.over {
			return
		}
		if len(s.body)+len(b) > maxJSON {
			s.over, s.body = true, nil
			return
		}
		s.body = append(s.body, b...)
		return
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if len(s.line)+len(b) <= maxStreamLine {
				s.line = append(s.line, b...)
			} else {
				s.line = s.line[:0]
			}
			return
		}
		if len(s.line)+i <= maxStreamLine {
			s.line = append(s.line, b[:i]...)
			s.streamLine(s.line)
		}
		s.line = s.line[:0]
		b = b[i+1:]
	}
}

func (s *usageSniffer) streamLine(line []byte) {
	data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("data:"))
	if !ok || !bytes.Contains(data, []byte(`"usage"`)) {
		return
	}
	s.take(bytes.TrimSpace(data))
}

func (s *usageSniffer) take(b []byte) {
	var v struct {
		Usage *usage `json:"usage"`
	}
	if json.Unmarshal(b, &v) == nil && v.Usage != nil {
		s.got = *v.Usage
	}
}

func (s *usageSniffer) finish() {
	if s.done {
		return
	}
	s.done = true
	if !s.stream && !s.over {
		s.take(s.body)
	}
	s.body, s.line = nil, nil
}
