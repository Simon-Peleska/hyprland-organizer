const std = @import("std");
const hyprland = @import("hyprland.zig");
const types = @import("types.zig");
const fmt = std.fmt.allocPrint;
const eql = std.mem.eql;
const Allocator = std.mem.Allocator;

const AppError = error{
    OutOfMemory,
    FileSystemError,
};

pub fn main() !void {
    var arena_allocator = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena_allocator.deinit();
    const arena = arena_allocator.allocator();

    const args = try std.process.argsAlloc(arena);

    const listen = args.len >= 2 and eql(u8, args[1], "--listen");
    const app_groups = args[(if (listen) 2 else 1)..];

    organizeWorkspaces(arena, app_groups);

    if (listen) try hyprland.listenForEvents(arena, eventHandler, app_groups);
}

fn eventHandler(allocator: Allocator, line: []const u8, app_groups: []const []const u8) void {
    if (!std.mem.startsWith(u8, line, "monitoraddedv2") and !std.mem.startsWith(u8, line, "monitorremovedv2")) return;

    std.time.sleep(200 * 1000 * 1000); // 200ms
    organizeWorkspaces(allocator, app_groups);
}

fn organizeWorkspaces(arena: Allocator, app_groups: []const []const u8) void {
    const active_window = hyprland.getActiveWindow(arena) catch |err| {
        std.log.err("Failed to get active window: {any}", .{err});
        return null;
    };
    const cursor_position = hyprland.getCursorPosition(arena) catch |err| {
        std.log.err("Failed to get cursor position: {any}", .{err});
        return null;
    };

    moveWorkspaces(arena) catch |err| {
        std.log.err("Failed to move workspaces: {any}", .{err});
        return;
    };
    moveApplications(arena, app_groups) catch |err| {
        std.log.err("Failed to move applications: {any}", .{err});
        return;
    };

    if (active_window != null and cursor_position != null) {
        setMouseToActiveWindow(arena, active_window.?, cursor_position.?) catch |err| {
            std.log.err("Failed to set mouse to active window: {any}", .{err});
            return;
        };
    } else {
        _ = hyprland.hyprlandCommand(arena, "dispatch workspace 1") catch |err| {
            std.log.err("Failed to dispatch workspace 1: {any}", .{err});
            return;
        };
    }
}

fn moveWorkspaces(arena: Allocator) !void {
    const monitors = try hyprland.getMonitors(arena);

    std.sort.block(types.Monitor, monitors, {}, types.Monitor.monitorLessThan);
    for (monitors, 1..) |monitor, i| {
        const command = try fmt(arena, "dispatch workspace {d}", .{ i });
        _ = try hyprland.hyprlandCommand(arena, command);
        const command2 = try fmt(arena, "dispatch moveworkspacetomonitor {d} {d}", .{ i, monitor.id });
        _ = try hyprland.hyprlandCommand(arena, command2);
    }
}

fn moveApplications(arena: Allocator, app_groups: []const []const u8) !void {
    const clients = try hyprland.getClients(arena);

    for (app_groups, 1..) |app_group, i| {
        if (eql(u8, app_group, "skip")) continue;

        var apps = std.mem.splitScalar(u8, app_group, ',');
        while (apps.next()) |app| {
            var already_started = false;
            var command: ?[]u8 = null;

            for (clients) |client| {
                if (std.mem.indexOf(u8, client.class, app) == null) continue;

                already_started = true;

                if (client.workspace.id == i) break;

                command = try fmt(arena, "dispatch movetoworkspacesilent {d},address:{s}", .{ i, client.address });
                break;
            }

            if (!already_started) command = try fmt(arena, "dispatch exec [workspace {d} silent] {s}", .{ i, app });
            if (command) |command_result| _ = try hyprland.hyprlandCommand(arena, command_result);
        }
    }
}

fn setMouseToActiveWindow(arena: Allocator, active_window_old: types.Client, cursor_position: types.CursorPosition) !void {
    const command = try fmt(arena, "dispatch focuswindow address:{s}", .{active_window_old.address});
    _ = try hyprland.hyprlandCommand(arena, command);

    const active_window_new = try hyprland.getActiveWindow(arena) orelse return;

    const relative_x: f32 = (cursor_position.x - active_window_old.at[0]) * (active_window_new.size[0] / active_window_old.size[0]);
    const relative_y: f32 = (cursor_position.y - active_window_old.at[1]) * (active_window_new.size[1] / active_window_old.size[1]);

    if (relative_x <= active_window_new.size[0] and relative_y <= active_window_new.size[1]) {
        const x: i32 = @intFromFloat(relative_x + active_window_new.at[0]);
        const y: i32 = @intFromFloat(relative_y + active_window_new.at[1]);

        const mouse_command = try fmt(arena, "dispatch movecursor {d} {d}", .{ x, y });
        _ = try hyprland.hyprlandCommand(arena, mouse_command);
    }
    
}

