package firewalla

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// stub is a Redis that answers the two commands this plugin sends, over a real
// socket. net.Pipe cannot be used: pipelining writes a batch of commands before
// reading any reply, and an unbuffered pipe deadlocks on the first one.
type stub struct {
	hashes map[string]map[string]string

	// broken keys answer HGETALL with an error, which is what one unreadable
	// device looks like from here.
	broken map[string]bool

	// page is how many keys one SCAN step returns, so the cursor loop is
	// exercised rather than assumed.
	page int
}

// listen starts the stub and returns an Open that dials it.
func (s *stub) listen(t *testing.T) Open {
	t.Helper()

	if s.page == 0 {
		s.page = 2
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()

	address := listener.Addr().String()
	return func(ctx context.Context) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, "tcp", address)
	}
}

func (s *stub) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
		command, err := readCommand(reader)
		if err != nil {
			return
		}
		if len(command) == 0 {
			return
		}

		s.answer(writer, command)
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func (s *stub) answer(w *bufio.Writer, command []string) {
	switch strings.ToUpper(command[0]) {
	case "SCAN":
		s.scan(w, command)
	case "HGETALL":
		s.hgetall(w, command[1])
	default:
		say(w,"-ERR this stub answers SCAN and HGETALL, not %s\r\n", command[0])
	}
}

func (s *stub) scan(w *bufio.Writer, command []string) {
	pattern := ""
	for i := 2; i+1 < len(command); i += 2 {
		if strings.EqualFold(command[i], "MATCH") {
			pattern = command[i+1]
		}
	}

	matched := s.matching(pattern)
	cursor, err := strconv.Atoi(command[1])
	if err != nil || cursor < 0 || cursor > len(matched) {
		say(w,"-ERR invalid cursor\r\n")
		return
	}

	end := min(cursor+s.page, len(matched))
	next := end
	if end == len(matched) {
		next = 0
	}

	say(w,"*2\r\n")
	bulk(w, strconv.Itoa(next))
	say(w,"*%d\r\n", end-cursor)
	for _, key := range matched[cursor:end] {
		bulk(w, key)
	}
}

func (s *stub) matching(pattern string) []string {
	keys := make([]string, 0, len(s.hashes))
	for key := range s.hashes {
		if pattern == "" || strings.HasPrefix(key, strings.TrimSuffix(pattern, "*")) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (s *stub) hgetall(w *bufio.Writer, key string) {
	if s.broken[key] {
		say(w,"-ERR that key is having a bad day\r\n")
		return
	}

	fields := s.hashes[key]
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	say(w,"*%d\r\n", len(names)*2)
	for _, name := range names {
		bulk(w, name)
		bulk(w, fields[name])
	}
}

func bulk(w *bufio.Writer, value string) {
	say(w, "$%d\r\n%s\r\n", len(value), value)
}

// say writes to a buffer whose failures surface as the client's read failing,
// which is what a test would be asserting on anyway.
func say(w *bufio.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func readCommand(r *bufio.Reader) ([]string, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(header, "*") {
		return nil, fmt.Errorf("stub: want an array, got %q", header)
	}

	count, err := strconv.Atoi(strings.TrimSpace(header[1:]))
	if err != nil {
		return nil, err
	}

	command := make([]string, 0, count)
	for range count {
		size, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(strings.TrimSpace(size[1:]))
		if err != nil {
			return nil, err
		}

		body := make([]byte, length+2)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		command = append(command, string(body[:length]))
	}
	return command, nil
}
