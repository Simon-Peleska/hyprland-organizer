package main

// zwlr_screencopy_manager_v1 and output geometry helpers.

import (
	"fmt"
	"image"
	"image/color"

	"golang.org/x/sys/unix"
)

const (
	wlShmFormatARGB8888 = 0
	wlShmFormatXRGB8888 = 1
	wlShmFormatXBGR8888 = 0x34324258
	wlShmFormatABGR8888 = 0x34324241
)

// captureOutput captures the given wl_output (by registry name) and returns an RGBA image.
func captureOutput(wl *waylandConn, reg *wlRegistry, outputRegName uint32) (*image.RGBA, error) {
	outputID, err := reg.bind(outputRegName, "wl_output", 4)
	if err != nil {
		return nil, fmt.Errorf("bind wl_output: %w", err)
	}
	wl.register(outputID, &nullDispatcher{})

	mgr, ok := reg.findGlobal("zwlr_screencopy_manager_v1")
	if !ok {
		return nil, fmt.Errorf("compositor does not support zwlr_screencopy_manager_v1")
	}
	mgrID, err := reg.bind(mgr.name, "zwlr_screencopy_manager_v1", 3)
	if err != nil {
		return nil, fmt.Errorf("bind zwlr_screencopy_manager_v1: %w", err)
	}

	shmGlobal, ok := reg.findGlobal("wl_shm")
	if !ok {
		return nil, fmt.Errorf("compositor does not advertise wl_shm")
	}
	shmID, err := reg.bind(shmGlobal.name, "wl_shm", 1)
	if err != nil {
		return nil, fmt.Errorf("bind wl_shm: %w", err)
	}
	wl.register(shmID, &nullDispatcher{})

	frameID := wl.alloc()
	frame := &screencopyFrame{}
	wl.register(frameID, frame)

	// capture_output: opcode 0 on manager
	args := make([]byte, 3*4)
	off := 0
	off = scPutU32(args, off, frameID)
	off = scPutU32(args, off, 0) // no cursor overlay
	_ = scPutU32(args, off, outputID)
	if err := wl.send(mgrID, 0, args, -1); err != nil {
		return nil, fmt.Errorf("capture_output: %w", err)
	}

	useV3 := mgr.version >= 3
	if useV3 {
		for !frame.bufferDone {
			if err := wl.recv(); err != nil {
				return nil, fmt.Errorf("waiting for buffer_done: %w", err)
			}
			if frame.failed {
				return nil, fmt.Errorf("screencopy frame failed before buffer_done")
			}
		}
	} else {
		for !frame.hasBuffer {
			if err := wl.recv(); err != nil {
				return nil, fmt.Errorf("waiting for buffer event: %w", err)
			}
			if frame.failed {
				return nil, fmt.Errorf("screencopy frame failed")
			}
		}
	}

	switch frame.format {
	case wlShmFormatARGB8888, wlShmFormatXRGB8888, wlShmFormatXBGR8888, wlShmFormatABGR8888:
	default:
		return nil, fmt.Errorf("unsupported shm format: 0x%x", frame.format)
	}

	w, h, stride := int(frame.width), int(frame.height), int(frame.stride)
	shmSize := stride * h

	fd, err := wlShmCreate(shmSize)
	if err != nil {
		return nil, fmt.Errorf("shm create: %w", err)
	}
	defer unix.Close(fd)

	data, err := wlMmap(fd, shmSize)
	if err != nil {
		return nil, fmt.Errorf("mmap: %w", err)
	}
	defer wlMunmap(data)

	poolID := wl.alloc()
	{
		args := make([]byte, 8)
		scPutU32(args, 0, poolID)
		scPutI32(args, 4, int32(shmSize))
		if err := wl.send(shmID, 0, args, fd); err != nil {
			return nil, fmt.Errorf("wl_shm.create_pool: %w", err)
		}
		wl.register(poolID, &nullDispatcher{})
	}

	bufID := wl.alloc()
	{
		args := make([]byte, 6*4)
		off := 0
		off = scPutU32(args, off, bufID)
		off = scPutI32(args, off, 0)
		off = scPutI32(args, off, int32(w))
		off = scPutI32(args, off, int32(h))
		off = scPutI32(args, off, int32(stride))
		_ = scPutU32(args, off, frame.format)
		if err := wl.send(poolID, 0, args, -1); err != nil {
			return nil, fmt.Errorf("wl_shm_pool.create_buffer: %w", err)
		}
		wl.register(bufID, &nullDispatcher{})
	}

	wl.send(poolID, 1, nil, -1) // destroy pool

	// zwlr_screencopy_frame_v1.copy(buffer) — opcode 0
	{
		args := wlEncodeUint32(bufID)
		if err := wl.send(frameID, 0, args, -1); err != nil {
			return nil, fmt.Errorf("screencopy_frame.copy: %w", err)
		}
	}

	for !frame.ready && !frame.failed {
		if err := wl.recv(); err != nil {
			return nil, fmt.Errorf("waiting for frame ready: %w", err)
		}
	}
	if frame.failed {
		return nil, fmt.Errorf("screencopy frame capture failed")
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	yInvert := frame.flags&1 != 0

	for y := 0; y < h; y++ {
		srcRow := y
		if yInvert {
			srcRow = h - 1 - y
		}
		for x := 0; x < w; x++ {
			off := srcRow*stride + x*4
			var r, g, b byte
			switch frame.format {
			case wlShmFormatARGB8888, wlShmFormatXRGB8888:
				b = data[off+0]
				g = data[off+1]
				r = data[off+2]
			case wlShmFormatABGR8888, wlShmFormatXBGR8888:
				r = data[off+0]
				g = data[off+1]
				b = data[off+2]
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}

	wl.send(frameID, 1, nil, -1) // frame.destroy
	wl.send(bufID, 0, nil, -1)   // buffer.destroy

	return img, nil
}

type screencopyFrame struct {
	hasBuffer  bool
	format     uint32
	width      uint32
	height     uint32
	stride     uint32
	bufferDone bool
	flags      uint32
	ready      bool
	failed     bool
}

func (f *screencopyFrame) dispatch(opcode uint16, data []byte, _ int) {
	switch opcode {
	case 0: // buffer(format, width, height, stride)
		if len(data) < 16 {
			return
		}
		f.format = scReadU32(data, 0)
		f.width = scReadU32(data, 4)
		f.height = scReadU32(data, 8)
		f.stride = scReadU32(data, 12)
		f.hasBuffer = true
	case 1: // flags
		if len(data) >= 4 {
			f.flags = scReadU32(data, 0)
		}
	case 2: // ready
		f.ready = true
	case 3: // failed
		f.failed = true
	case 6: // buffer_done (v3)
		f.bufferDone = true
	}
}

// outputGeom stores the logical position and size of a wl_output.
type outputGeom struct {
	regName uint32
	x, y    int
	w, h    int
}

func (g *outputGeom) dispatch(opcode uint16, data []byte, _ int) {
	switch opcode {
	case 0: // geometry: x, y, ...
		if len(data) >= 8 {
			g.x = int(scReadI32(data, 0))
			g.y = int(scReadI32(data, 4))
		}
	case 1: // mode: flags, w, h, refresh
		if len(data) >= 12 && scReadU32(data, 0)&1 != 0 {
			g.w = int(scReadI32(data, 4))
			g.h = int(scReadI32(data, 8))
		}
	}
}

type xdgOutputDispatcher struct {
	geom *outputGeom
}

func (d *xdgOutputDispatcher) dispatch(opcode uint16, data []byte, _ int) {
	switch opcode {
	case 0: // logical_position(x, y)
		if len(data) >= 8 {
			d.geom.x = int(scReadI32(data, 0))
			d.geom.y = int(scReadI32(data, 4))
		}
	case 1: // logical_size(w, h)
		if len(data) >= 8 {
			d.geom.w = int(scReadI32(data, 0))
			d.geom.h = int(scReadI32(data, 4))
		}
	}
}

func gatherOutputs(wl *waylandConn, reg *wlRegistry) ([]*outputGeom, error) {
	var wlOutputs []wlRegistryGlobal
	for _, g := range reg.globals {
		if g.iface == "wl_output" {
			wlOutputs = append(wlOutputs, g)
		}
	}
	if len(wlOutputs) == 0 {
		return nil, fmt.Errorf("no wl_output globals advertised")
	}

	var geoms []*outputGeom

	xdgMgr, hasXdg := reg.findGlobal("zxdg_output_manager_v1")
	if hasXdg {
		xdgMgrID, err := reg.bind(xdgMgr.name, "zxdg_output_manager_v1", 2)
		if err != nil {
			return nil, err
		}
		wl.register(xdgMgrID, &nullDispatcher{})

		for _, g := range wlOutputs {
			outID, err := reg.bind(g.name, "wl_output", 2)
			if err != nil {
				return nil, err
			}
			wl.register(outID, &nullDispatcher{})

			xdgOutID := wl.alloc()
			args := make([]byte, 8)
			scPutU32(args, 0, xdgOutID)
			scPutU32(args, 4, outID)
			if err := wl.send(xdgMgrID, 1, args, -1); err != nil {
				return nil, err
			}

			geom := &outputGeom{regName: g.name}
			geoms = append(geoms, geom)
			wl.register(xdgOutID, &xdgOutputDispatcher{geom: geom})
		}
	} else {
		for _, g := range wlOutputs {
			id, err := reg.bind(g.name, "wl_output", 2)
			if err != nil {
				return nil, err
			}
			geom := &outputGeom{regName: g.name}
			geoms = append(geoms, geom)
			wl.register(id, geom)
		}
	}

	if err := wl.roundtrip(); err != nil {
		return nil, err
	}
	return geoms, nil
}

func scPutU32(b []byte, off int, v uint32) int {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
	b[off+2] = byte(v >> 16)
	b[off+3] = byte(v >> 24)
	return off + 4
}

func scPutI32(b []byte, off int, v int32) int {
	return scPutU32(b, off, uint32(v))
}

func scReadU32(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

func scReadI32(b []byte, off int) int32 {
	return int32(scReadU32(b, off))
}
