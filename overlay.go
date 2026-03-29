package main

// Screenshot overlay using zwlr_layer_shell_v1.
//
// Before a workspace switch we capture each output and show the screenshots at
// two layers:
//   - OVERLAY (3): covers everything during the switch and window reordering.
//   - BOTTOM  (1): stays visible after OVERLAY drops, covering the background
//     while resized clients (e.g. Chromium) commit their first new buffer.

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type overlayHandle struct {
	wl         *waylandConn
	surfaces   []uint32 // OVERLAY wl_surface IDs
	lsurfaces  []uint32 // OVERLAY zwlr_layer_surface IDs
	bsurfaces  []uint32 // BOTTOM wl_surface IDs
	blsurfaces []uint32 // BOTTOM zwlr_layer_surface IDs
	buffers    []uint32 // all wl_buffer IDs (OVERLAY + BOTTOM)
	mmaps      [][]byte
}

func (h *overlayHandle) destroy() {
	// Drop the OVERLAY so windows become visible on top of the BOTTOM layer.
	for i := range h.lsurfaces {
		h.wl.send(h.lsurfaces[i], 7, nil, -1) // zwlr_layer_surface_v1.destroy
		h.wl.send(h.surfaces[i], 0, nil, -1)  // wl_surface.destroy
	}
	h.wl.roundtrip()

	// Wait two vsync frames on the BOTTOM surfaces. The BOTTOM layer is visible
	// wherever clients haven't yet painted at their new size, covering the
	// background. Two frame callbacks give resizing clients time to commit.
	for range 10 {
		n := len(h.bsurfaces)
		for _, surfID := range h.bsurfaces {
			cbID := h.wl.alloc()
			h.wl.callbacks[cbID] = func() { n-- }
			args := make([]byte, 4)
			scPutU32(args, 0, cbID)
			h.wl.send(surfID, 3, args, -1) // wl_surface.frame
			h.wl.send(surfID, 6, nil, -1)  // wl_surface.commit
		}
		for n > 0 {
			if err := h.wl.recv(); err != nil {
				break
			}
		}
	}

	// Drop the BOTTOM layer.
	for i := range h.blsurfaces {
		h.wl.send(h.blsurfaces[i], 7, nil, -1) // zwlr_layer_surface_v1.destroy
		h.wl.send(h.bsurfaces[i], 0, nil, -1)  // wl_surface.destroy
	}
	for _, bufID := range h.buffers {
		h.wl.send(bufID, 0, nil, -1) // wl_buffer.destroy
	}
	h.wl.roundtrip()
	for _, m := range h.mmaps {
		wlMunmap(m)
	}
	h.wl.close()
}

type lsResult struct {
	surfID  uint32
	lsurfID uint32
	bufID   uint32
}

// createLayerSurface sets up a zwlr_layer_shell surface displaying the
// screenshot stored in poolID at the given layer (1=BOTTOM, 3=OVERLAY).
// It waits for the configure event, attaches the buffer, and registers a frame
// callback that decrements *framePending when the compositor has rendered it.
func createLayerSurface(wl *waylandConn, compID, lsID, outID, poolID uint32,
	w, h, stride int, layer uint32, framePending *int) (lsResult, error) {

	surfID := wl.alloc()
	{
		args := make([]byte, 4)
		scPutU32(args, 0, surfID)
		wl.send(compID, 0, args, -1)
	}
	wl.register(surfID, &nullDispatcher{})

	lsurfID := wl.alloc()
	ls := &layerSurface{}
	wl.register(lsurfID, ls)
	{
		ns := wlEncodeString("hyprland-organizer")
		args := make([]byte, 16+len(ns))
		off := scPutU32(args, 0, lsurfID)
		off = scPutU32(args, off, surfID)
		off = scPutU32(args, off, outID)
		off = scPutU32(args, off, layer)
		copy(args[off:], ns)
		wl.send(lsID, 0, args, -1)
	}
	// set_size(0, 0) — compositor fills to output size
	{
		args := make([]byte, 8)
		wl.send(lsurfID, 0, args, -1)
	}
	// set_anchor(top|bottom|left|right = 15)
	{
		args := make([]byte, 4)
		scPutU32(args, 0, 15)
		wl.send(lsurfID, 1, args, -1)
	}
	// set_exclusive_zone(-1) — don't push other surfaces
	{
		args := make([]byte, 4)
		scPutI32(args, 0, -1)
		wl.send(lsurfID, 2, args, -1)
	}
	// initial commit → triggers configure
	wl.send(surfID, 6, nil, -1)

	for !ls.configured {
		if err := wl.recv(); err != nil {
			return lsResult{}, err
		}
	}

	// ack_configure — opcode 6
	{
		args := make([]byte, 4)
		scPutU32(args, 0, ls.serial)
		wl.send(lsurfID, 6, args, -1)
	}

	// Create buffer from pool (ARGB8888)
	bufID := wl.alloc()
	{
		args := make([]byte, 24)
		off := scPutU32(args, 0, bufID)
		off = scPutI32(args, off, 0)
		off = scPutI32(args, off, int32(w))
		off = scPutI32(args, off, int32(h))
		off = scPutI32(args, off, int32(stride))
		scPutU32(args, off, 0) // ARGB8888
		wl.send(poolID, 0, args, -1)
		wl.register(bufID, &nullDispatcher{})
	}

	// set_buffer_scale for HiDPI
	if ls.width > 0 && uint32(w) > ls.width {
		if scale := int32(w) / int32(ls.width); scale > 1 {
			args := make([]byte, 4)
			scPutI32(args, 0, scale)
			wl.send(surfID, 8, args, -1) // wl_surface.set_buffer_scale
		}
	}

	// wl_surface.attach(buffer, 0, 0) — opcode 1
	{
		args := make([]byte, 12)
		scPutU32(args, 0, bufID)
		wl.send(surfID, 1, args, -1)
	}
	// wl_surface.damage_buffer(0, 0, w, h) — opcode 9
	{
		args := make([]byte, 16)
		scPutI32(args, 8, int32(w))
		scPutI32(args, 12, int32(h))
		wl.send(surfID, 9, args, -1)
	}
	// wl_surface.frame — opcode 3 — callback fires when this frame is displayed
	cbID := wl.alloc()
	*framePending++
	{
		args := make([]byte, 4)
		scPutU32(args, 0, cbID)
		wl.send(surfID, 3, args, -1)
	}
	wl.callbacks[cbID] = func() { *framePending-- }
	// wl_surface.commit — opcode 6
	wl.send(surfID, 6, nil, -1)

	return lsResult{surfID, lsurfID, bufID}, nil
}

// showScreenshotOverlay captures all outputs and covers them with their
// screenshots at OVERLAY and BOTTOM layers. Returns a cleanup function.
func showScreenshotOverlay() (func(), error) {
	wl, err := waylandConnect()
	if err != nil {
		return nil, err
	}

	reg, err := wlGetRegistry(wl)
	if err != nil {
		wl.close()
		return nil, err
	}

	compID, err := reg.bindGlobal("wl_compositor", 4)
	if err != nil {
		wl.close()
		return nil, err
	}

	shmID, err := reg.bindGlobal("wl_shm", 1)
	if err != nil {
		wl.close()
		return nil, err
	}

	lsID, err := reg.bindGlobal("zwlr_layer_shell_v1", 4)
	if err != nil {
		wl.close()
		return nil, err
	}

	outputs, err := gatherOutputs(wl, reg)
	if err != nil {
		wl.close()
		return nil, err
	}

	handle := &overlayHandle{wl: wl}
	framePending := 0

	for _, out := range outputs {
		img, err := captureOutput(wl, reg, out.regName)
		if err != nil {
			continue
		}

		// Bind the output object for layer shell association.
		outID, err := reg.bind(out.regName, "wl_output", 4)
		if err != nil {
			continue
		}
		wl.register(outID, &nullDispatcher{})

		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		stride := w * 4
		shmSize := stride * h

		fd, err := wlShmCreate(shmSize)
		if err != nil {
			continue
		}
		data, err := wlMmap(fd, shmSize)
		if err != nil {
			unix.Close(fd)
			continue
		}

		// Write pixels as ARGB8888 (BGRA in memory, little-endian).
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				si := img.PixOffset(x, y)
				di := y*stride + x*4
				data[di+0] = img.Pix[si+2] // B
				data[di+1] = img.Pix[si+1] // G
				data[di+2] = img.Pix[si+0] // R
				data[di+3] = 255            // A (fully opaque)
			}
		}

		// Create a single shm pool shared by both layer surfaces.
		poolID := wl.alloc()
		{
			args := make([]byte, 8)
			scPutU32(args, 0, poolID)
			scPutI32(args, 4, int32(shmSize))
			wl.send(shmID, 0, args, fd)
			wl.register(poolID, &nullDispatcher{})
		}
		unix.Close(fd)

		// BOTTOM layer — stays visible after OVERLAY drops to cover repaints.
		bres, err := createLayerSurface(wl, compID, lsID, outID, poolID, w, h, stride, 1, &framePending)
		if err != nil {
			wl.send(poolID, 1, nil, -1)
			wlMunmap(data)
			continue
		}

		// OVERLAY layer — covers everything during switch and window reordering.
		res, err := createLayerSurface(wl, compID, lsID, outID, poolID, w, h, stride, 3, &framePending)
		if err != nil {
			wl.send(bres.lsurfID, 7, nil, -1)
			wl.send(bres.surfID, 0, nil, -1)
			wl.send(bres.bufID, 0, nil, -1)
			wl.send(poolID, 1, nil, -1)
			wlMunmap(data)
			continue
		}

		wl.send(poolID, 1, nil, -1) // wl_shm_pool.destroy

		handle.surfaces = append(handle.surfaces, res.surfID)
		handle.lsurfaces = append(handle.lsurfaces, res.lsurfID)
		handle.bsurfaces = append(handle.bsurfaces, bres.surfID)
		handle.blsurfaces = append(handle.blsurfaces, bres.lsurfID)
		handle.buffers = append(handle.buffers, res.bufID, bres.bufID)
		handle.mmaps = append(handle.mmaps, data)
	}

	if len(handle.surfaces) == 0 {
		wl.close()
		return nil, fmt.Errorf("no overlays created")
	}

	// Wait until the compositor has rendered our overlay frames on every output.
	for framePending > 0 {
		if err := wl.recv(); err != nil {
			break
		}
	}

	return handle.destroy, nil
}

// waitForWorkspaceSwitch blocks until Hyprland reports the target workspace
// is active, or the timeout elapses.
func waitForWorkspaceSwitch(targetID int) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	socketPath := fmt.Sprintf("%s/hypr/%s/.socket2.sock", runtimeDir, sig)

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	target := fmt.Sprintf("workspacev2>>%d,", targetID)

	buf := make([]byte, 4096)
	var partial string
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			partial += string(buf[:n])
			for {
				idx := strings.Index(partial, "\n")
				if idx < 0 {
					break
				}
				line := partial[:idx]
				partial = partial[idx+1:]
				if strings.HasPrefix(line, target) {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

type layerSurface struct {
	serial     uint32
	width      uint32
	height     uint32
	configured bool
}

func (l *layerSurface) dispatch(opcode uint16, data []byte, _ int) {
	switch opcode {
	case 0: // configure(serial, width, height)
		if len(data) >= 12 {
			l.serial = scReadU32(data, 0)
			l.width = scReadU32(data, 4)
			l.height = scReadU32(data, 8)
			l.configured = true
		}
	}
}
