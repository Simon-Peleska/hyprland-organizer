package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

const managedTag = "ho-managed"

type Client struct {
	Address   string     `json:"address"`
	Class     string     `json:"class"`
	Tags      []string   `json:"tags"`
	At        [2]float64 `json:"at"`
	Size      [2]float64 `json:"size"`
	Workspace struct {
		ID int `json:"id"`
	} `json:"workspace"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <workspace> app1 app2 app3\n", os.Args[0])
		os.Exit(1)
	}

	apps := os.Args[2:]
	workspace := struct{ ID int }{}
	fmt.Sscanf(os.Args[1], "%d", &workspace.ID)
	active := query[struct{ ID int }]("j/activeworkspace")
	if active.ID != workspace.ID {
		hyprctl(fmt.Sprintf("dispatch workspace %d", workspace.ID))
	}
	clients := query[[]Client]("j/clients")
	var launched []string

	for _, app := range apps {
		// Reuse existing tagged instance
		if c := findTagged(clients, app); c != nil {
			if c.Workspace.ID != workspace.ID {
				hyprctl(fmt.Sprintf("dispatch movetoworkspacesilent %d,address:%s", workspace.ID, c.Address))
			}
			continue
		}

		// Launch new instance
		hyprctl(fmt.Sprintf("dispatch exec [workspace %d silent] %s", workspace.ID, app))
		launched = append(launched, app)
	}

	if len(launched) > 0 {
		tagNewClients(launched)
	}

	// Save cursor position before reordering (focuswindow warps cursor)
	pos := query[struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}]("j/cursorpos")

	enforceOrder(apps, workspace.ID)

	// Focus the window under the cursor and restore cursor position
	updatedClients := query[[]Client]("j/clients")
	for _, c := range updatedClients {
		if c.Workspace.ID == workspace.ID &&
			pos.X >= c.At[0] && pos.X < c.At[0]+c.Size[0] &&
			pos.Y >= c.At[1] && pos.Y < c.At[1]+c.Size[1] {
			hyprctl(fmt.Sprintf("dispatch focuswindow address:%s", c.Address))
			break
		}
	}
	hyprctl(fmt.Sprintf("dispatch movecursor %d %d", int(pos.X), int(pos.Y)))
}

func enforceOrder(apps []string, workspaceID int) {
	clients := query[[]Client]("j/clients")

	// Collect clients on this workspace matching our apps, in parameter order
	type entry struct {
		address string
		x       float64
	}
	var entries []entry
	for _, app := range apps {
		appLower := strings.ToLower(app)
		for _, c := range clients {
			if c.Workspace.ID == workspaceID && strings.Contains(strings.ToLower(c.Class), appLower) {
				entries = append(entries, entry{c.Address, c.At[0]})
				break
			}
		}
	}

	if len(entries) < 2 {
		return
	}

	// Get the target x-positions by sorting current positions
	sorted := make([]float64, len(entries))
	for i, e := range entries {
		sorted[i] = e.x
	}
	sort.Float64s(sorted)

	// Swap windows that aren't in the right position
	// Focus each window in reverse parameter order so the first ends up leftmost
	for i := len(entries) - 1; i >= 0; i-- {
		hyprctl(fmt.Sprintf("dispatch focuswindow address:%s", entries[i].address))
		hyprctl("dispatch movewindow l")
	}
}

func findTagged(clients []Client, app string) *Client {
	app = strings.ToLower(app)
	for i := range clients {
		c := &clients[i]
		if strings.Contains(strings.ToLower(c.Class), app) && hasTag(c) {
			return c
		}
	}
	return nil
}

func hasTag(c *Client) bool {
	for _, t := range c.Tags {
		if t == managedTag {
			return true
		}
	}
	return false
}

func tagNewClients(launched []string) {
	remaining := make(map[string]bool, len(launched))
	for _, app := range launched {
		remaining[strings.ToLower(app)] = true
	}

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	socketPath := fmt.Sprintf("%s/hypr/%s/.socket2.sock", runtimeDir, sig)

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		log.Printf("failed to connect to event socket: %v", err)
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))

	buf := make([]byte, 4096)
	var partial string
	for len(remaining) > 0 {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		partial += string(buf[:n])
		for {
			idx := strings.Index(partial, "\n")
			if idx < 0 {
				break
			}
			line := partial[:idx]
			partial = partial[idx+1:]

			// openwindow>>ADDRESS,WORKSPACE,CLASS,TITLE
			if !strings.HasPrefix(line, "openwindow>>") {
				continue
			}
			parts := strings.SplitN(line[len("openwindow>>"):], ",", 4)
			if len(parts) < 3 {
				continue
			}
			address := "0x" + parts[0]
			class := strings.ToLower(parts[2])

			for app := range remaining {
				if strings.Contains(class, app) {
					hyprctl(fmt.Sprintf("dispatch tagwindow +%s address:%s", managedTag, address))
					delete(remaining, app)
					break
				}
			}
		}
	}
}

func query[T any](cmd string) T {
	data := hyprctl(cmd)
	var result T
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		log.Fatalf("failed to parse %s response: %v", cmd, err)
	}
	return result
}

func hyprctl(cmd string) string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	if runtimeDir == "" || sig == "" {
		log.Fatal("XDG_RUNTIME_DIR and HYPRLAND_INSTANCE_SIGNATURE must be set")
	}

	socketPath := fmt.Sprintf("%s/hypr/%s/.socket.sock", runtimeDir, sig)
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		log.Fatalf("failed to connect to hyprland socket: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(cmd)); err != nil {
		log.Fatalf("failed to send command: %v", err)
	}

	resp, err := io.ReadAll(conn)
	if err != nil {
		log.Fatalf("failed to read response: %v", err)
	}
	return string(resp)
}
