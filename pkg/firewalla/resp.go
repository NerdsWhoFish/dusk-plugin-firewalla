package firewalla

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

// redis speaks RESP2, and only SCAN and HGETALL. A client that cannot build a
// write cannot be talked into one by a bug above it.
type redis struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func newRedis(conn net.Conn) *redis {
	return &redis{
		conn:   conn,
		reader: bufio.NewReaderSize(conn, 64<<10),
		writer: bufio.NewWriterSize(conn, 16<<10),
	}
}

// pipeline is how many commands go out before any reply is read. Sending all
// of them would deadlock once the far end stops draining, and one at a time
// pays a round trip per device.
const pipeline = 64

// scanCount is the server side batch size for one SCAN step. SCAN rather than
// KEYS is the whole point: KEYS blocks Redis for the length of the keyspace,
// and this Redis belongs to the router the house is behind.
const scanCount = 500

// hash is one Redis hash, field to value.
type hash map[string]string

// scan returns every key matching pattern, walking the cursor to completion.
func (r *redis) scan(pattern string) ([]string, error) {
	var keys []string
	cursor := "0"

	for {
		reply, err := r.call("SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(scanCount))
		if err != nil {
			return nil, err
		}
		if len(reply.array) != 2 {
			return nil, fmt.Errorf("firewalla: SCAN answered %d parts, want a cursor and a page", len(reply.array))
		}

		for _, key := range reply.array[1].array {
			keys = append(keys, string(key.bytes))
		}

		cursor = string(reply.array[0].bytes)
		if cursor == "0" {
			return keys, nil
		}
	}
}

// hashes reads many hashes, pipelined, and reports which keys failed rather
// than failing the lot. One unreadable key is a partial answer; treating it as
// no answer at all is how a catalog loses entities that are still there.
func (r *redis) hashes(keys []string) (map[string]hash, []error) {
	out := make(map[string]hash, len(keys))
	var failures []error

	for start := 0; start < len(keys); start += pipeline {
		batch := keys[start:min(start+pipeline, len(keys))]

		for _, key := range batch {
			if err := r.write("HGETALL", key); err != nil {
				return out, append(failures, err)
			}
		}
		if err := r.writer.Flush(); err != nil {
			return out, append(failures, err)
		}

		for _, key := range batch {
			reply, err := r.read()
			switch {
			case err != nil && isProtocol(err):
				return out, append(failures, err)
			case err != nil:
				failures = append(failures, fmt.Errorf("firewalla: read %s: %w", key, err))
			default:
				out[key] = flatten(reply)
			}
		}
	}

	return out, failures
}

// flatten turns RESP's field, value, field, value array into a hash. An odd
// length can only come from a Redis that answered something other than a hash,
// so the trailing field is dropped rather than paired with nothing.
func flatten(reply value) hash {
	out := make(hash, len(reply.array)/2)
	for i := 0; i+1 < len(reply.array); i += 2 {
		out[string(reply.array[i].bytes)] = string(reply.array[i+1].bytes)
	}
	return out
}

func (r *redis) call(name string, args ...string) (value, error) {
	if err := r.write(name, args...); err != nil {
		return value{}, err
	}
	if err := r.writer.Flush(); err != nil {
		return value{}, err
	}
	return r.read()
}

func (r *redis) write(name string, args ...string) error {
	if _, err := fmt.Fprintf(r.writer, "*%d\r\n", len(args)+1); err != nil {
		return err
	}
	for _, part := range append([]string{name}, args...) {
		if _, err := fmt.Fprintf(r.writer, "$%d\r\n%s\r\n", len(part), part); err != nil {
			return err
		}
	}
	return nil
}

// value is one RESP reply. Only the two shapes this plugin asks for are
// modelled: a string and an array of them.
type value struct {
	bytes []byte
	array []value
}

// errProtocol means the stream is no longer parseable, so the next reply
// cannot be trusted to line up with the next command.
var errProtocol = errors.New("firewalla: the Redis stream stopped making sense")

func isProtocol(err error) bool {
	return errors.Is(err, errProtocol) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (r *redis) read() (value, error) {
	line, err := r.line()
	if err != nil {
		return value{}, err
	}
	if len(line) == 0 {
		return value{}, errProtocol
	}

	body := string(line[1:])
	switch line[0] {
	case '+', ':':
		return value{bytes: line[1:]}, nil

	case '-':
		return value{}, redisError(body)

	case '$':
		size, err := strconv.Atoi(body)
		if err != nil {
			return value{}, fmt.Errorf("%w: bulk length %q", errProtocol, body)
		}
		if size < 0 {
			return value{}, nil
		}
		return r.bulk(size)

	case '*':
		count, err := strconv.Atoi(body)
		if err != nil {
			return value{}, fmt.Errorf("%w: array length %q", errProtocol, body)
		}
		if count < 0 {
			return value{}, nil
		}
		return r.elements(count)
	}

	return value{}, fmt.Errorf("%w: reply type %q", errProtocol, string(line[0]))
}

func (r *redis) bulk(size int) (value, error) {
	body := make([]byte, size+2)
	if _, err := io.ReadFull(r.reader, body); err != nil {
		return value{}, err
	}
	return value{bytes: body[:size]}, nil
}

func (r *redis) elements(count int) (value, error) {
	out := value{array: make([]value, 0, count)}
	for range count {
		element, err := r.read()
		if err != nil {
			return value{}, err
		}
		out.array = append(out.array, element)
	}
	return out, nil
}

// line reads one CRLF terminated line, with the terminator removed.
func (r *redis) line() ([]byte, error) {
	raw, err := r.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	for len(raw) > 0 && (raw[len(raw)-1] == '\n' || raw[len(raw)-1] == '\r') {
		raw = raw[:len(raw)-1]
	}
	return raw, nil
}

// redisError is what the server itself refused, kept distinct from a transport
// failure so "it said NOAUTH" does not read as "the box is unreachable".
type redisError string

func (e redisError) Error() string { return "firewalla: Redis refused the read: " + string(e) }
