package main

// Minimal Wayland wire protocol implementation.
//
// The Wayland protocol is message-passing over a Unix socket.
// Each message has an 8-byte header:
//   [0:4]  object ID (uint32 LE)
//   [4:8]  (size << 16) | opcode  (uint32 LE)
// Followed by (size - 8) bytes of arguments.
//
// File descriptors are passed out-of-band via Unix SCM_RIGHTS cmsg.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// conn wraps the Wayland Unix socket connection and tracks object IDs.
type waylandConn struct {
	c         *net.UnixConn
	nextID    uint32
	objects   map[uint32]dispatcher
	callbacks map[uint32]func() // wl_callback done handlers keyed by object ID
}

type dispatcher interface {
	dispatch(opcode uint16, data []byte, fd int)
}

func waylandConnect() (*waylandConn, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return nil, errors.New("XDG_RUNTIME_DIR not set")
	}
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		display = "wayland-0"
	}
	addr := runtimeDir + "/" + display

	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: addr, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}

	wl := &waylandConn{
		c:         c,
		nextID:    1, // ID 1 is the wl_display
		objects:   make(map[uint32]dispatcher),
		callbacks: make(map[uint32]func()),
	}
	return wl, nil
}

func (wl *waylandConn) close() {
	wl.c.Close()
}

// alloc reserves a new object ID.
func (wl *waylandConn) alloc() uint32 {
	wl.nextID++
	return wl.nextID
}

// register binds an object ID to a dispatcher.
func (wl *waylandConn) register(id uint32, d dispatcher) {
	wl.objects[id] = d
}

func (wl *waylandConn) unregister(id uint32) {
	delete(wl.objects, id)
}

// send writes a Wayland request message.
func (wl *waylandConn) send(objID uint32, opcode uint16, args []byte, fd int) error {
	size := uint32(8 + len(args))
	hdr := make([]byte, 8)
	binary.LittleEndian.PutUint32(hdr[0:4], objID)
	binary.LittleEndian.PutUint32(hdr[4:8], size<<16|uint32(opcode))
	msg := append(hdr, args...)

	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}

	n, oobn, err := wl.c.WriteMsgUnix(msg, oob, nil)
	if err != nil {
		return err
	}
	if n != len(msg) || (fd >= 0 && oobn != len(oob)) {
		return fmt.Errorf("short write: n=%d oobn=%d", n, oobn)
	}
	return nil
}

var oobSpace = unix.CmsgSpace(4 * 4)

// recv reads one Wayland event and dispatches it.
func (wl *waylandConn) recv() error {
	header := make([]byte, 8)
	oob := make([]byte, oobSpace)

	n, oobn, _, _, err := wl.c.ReadMsgUnix(header, oob)
	if err != nil {
		return err
	}
	if n != 8 {
		return fmt.Errorf("short header read: %d", n)
	}

	fd := -1
	if oobn > 0 {
		fds, err := wlParseFds(oob[:oobn])
		if err == nil && len(fds) > 0 {
			fd = fds[0]
		}
	}

	senderID := binary.LittleEndian.Uint32(header[0:4])
	sizeOpcode := binary.LittleEndian.Uint32(header[4:8])
	opcode := uint16(sizeOpcode & 0xffff)
	size := int(sizeOpcode >> 16)

	var body []byte
	bodyLen := size - 8
	if bodyLen > 0 {
		body = make([]byte, bodyLen)
		oob2 := make([]byte, oobSpace)
		var n2, oobn2 int
		if fd == -1 {
			n2, oobn2, _, _, err = wl.c.ReadMsgUnix(body, oob2)
		} else {
			n2, err = wl.c.Read(body)
		}
		if err != nil {
			return err
		}
		if n2 != bodyLen {
			return fmt.Errorf("short body read: got %d want %d", n2, bodyLen)
		}
		if fd == -1 && oobn2 > 0 {
			fds, err := wlParseFds(oob2[:oobn2])
			if err == nil && len(fds) > 0 {
				fd = fds[0]
			}
		}
	}

	// wl_display (ID=1) opcode 0 = error, opcode 1 = delete_id
	if senderID == 1 {
		if opcode == 0 && len(body) >= 12 {
			objID := binary.LittleEndian.Uint32(body[0:4])
			code := binary.LittleEndian.Uint32(body[4:8])
			msgLen := int(binary.LittleEndian.Uint32(body[8:12]))
			msg := ""
			if msgLen > 0 && len(body) >= 12+msgLen {
				msg = string(body[12 : 12+msgLen-1])
			}
			return fmt.Errorf("wl_display error: obj=%d code=%d msg=%q", objID, code, msg)
		}
		if opcode == 1 && len(body) >= 4 {
			id := binary.LittleEndian.Uint32(body[0:4])
			wl.unregister(id)
		}
		return nil
	}

	// wl_callback: opcode 0 = done
	if cb, ok := wl.callbacks[senderID]; ok {
		cb()
		delete(wl.callbacks, senderID)
		wl.unregister(senderID)
		return nil
	}

	d, ok := wl.objects[senderID]
	if !ok {
		return nil
	}
	d.dispatch(opcode, body, fd)
	return nil
}

// roundtrip sends a wl_display.sync and processes events until the callback fires.
func (wl *waylandConn) roundtrip() error {
	cbID := wl.alloc()
	done := false
	wl.callbacks[cbID] = func() { done = true }

	args := make([]byte, 4)
	binary.LittleEndian.PutUint32(args[0:4], cbID)
	if err := wl.send(1, 0, args, -1); err != nil {
		return err
	}

	for !done {
		if err := wl.recv(); err != nil {
			return err
		}
	}
	return nil
}

// --- Registry ---

type wlRegistryGlobal struct {
	name    uint32
	iface   string
	version uint32
}

type wlRegistry struct {
	wl      *waylandConn
	id      uint32
	globals []wlRegistryGlobal
}

func wlGetRegistry(wl *waylandConn) (*wlRegistry, error) {
	regID := wl.alloc()
	r := &wlRegistry{wl: wl, id: regID}
	wl.register(regID, r)

	args := make([]byte, 4)
	binary.LittleEndian.PutUint32(args[0:4], regID)
	if err := wl.send(1, 1, args, -1); err != nil {
		return nil, err
	}

	if err := wl.roundtrip(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *wlRegistry) dispatch(opcode uint16, data []byte, _ int) {
	switch opcode {
	case 0: // global
		if len(data) < 12 {
			return
		}
		name := binary.LittleEndian.Uint32(data[0:4])
		ifaceLen := int(binary.LittleEndian.Uint32(data[4:8]))
		if len(data) < 8+ifaceLen {
			return
		}
		iface := wlNullStr(data[8 : 8+ifaceLen])
		off := 8 + wlPadded(ifaceLen)
		if len(data) < off+4 {
			return
		}
		version := binary.LittleEndian.Uint32(data[off : off+4])
		r.globals = append(r.globals, wlRegistryGlobal{name, iface, version})
	}
}

func (r *wlRegistry) bind(name uint32, iface string, version uint32) (uint32, error) {
	newID := r.wl.alloc()

	ifaceBytes := wlEncodeString(iface)
	args := make([]byte, 4+len(ifaceBytes)+4+4)
	off := 0
	binary.LittleEndian.PutUint32(args[off:], name)
	off += 4
	copy(args[off:], ifaceBytes)
	off += len(ifaceBytes)
	binary.LittleEndian.PutUint32(args[off:], version)
	off += 4
	binary.LittleEndian.PutUint32(args[off:], newID)

	return newID, r.wl.send(r.id, 0, args, -1)
}

func (r *wlRegistry) findGlobal(iface string) (wlRegistryGlobal, bool) {
	for _, g := range r.globals {
		if g.iface == iface {
			return g, true
		}
	}
	return wlRegistryGlobal{}, false
}

func (r *wlRegistry) bindGlobal(iface string, version uint32) (uint32, error) {
	g, ok := r.findGlobal(iface)
	if !ok {
		return 0, fmt.Errorf("%s not found in registry", iface)
	}
	id, err := r.bind(g.name, iface, version)
	if err != nil {
		return 0, err
	}
	r.wl.register(id, &nullDispatcher{})
	return id, nil
}

// --- Encoding helpers ---

func wlEncodeString(s string) []byte {
	l := len(s) + 1
	p := wlPadded(l)
	b := make([]byte, 4+p)
	binary.LittleEndian.PutUint32(b[0:4], uint32(l))
	copy(b[4:], s)
	return b
}

func wlPadded(l int) int {
	return (l + 3) &^ 3
}

func wlNullStr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func wlEncodeUint32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func wlParseFds(oob []byte) ([]int, error) {
	scms, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	var fds []int
	for _, scm := range scms {
		got, err := unix.ParseUnixRights(&scm)
		if err != nil {
			continue
		}
		fds = append(fds, got...)
	}
	return fds, nil
}

func wlShmCreate(size int) (int, error) {
	fd, err := unix.MemfdCreate("hyprland-organizer-shm", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return wlShmCreateFallback(size)
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func wlShmCreateFallback(size int) (int, error) {
	name := fmt.Sprintf("/hyprland-organizer-%d", os.Getpid())
	fd, err := unix.Open("/dev/shm"+name, unix.O_RDWR|unix.O_CREAT|unix.O_TRUNC|unix.O_CLOEXEC, 0600)
	if err != nil {
		return -1, fmt.Errorf("shm fallback: %w", err)
	}
	unix.Unlink("/dev/shm" + name)
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func wlMmap(fd, size int) ([]byte, error) {
	return unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
}

func wlMunmap(b []byte) {
	unix.Munmap(b)
}

// nullDispatcher silently drops all events.
type nullDispatcher struct{}

func (n *nullDispatcher) dispatch(_ uint16, _ []byte, fd int) {
	if fd >= 0 {
		unix.Close(fd)
	}
}
