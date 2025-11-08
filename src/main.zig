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

    var listen = false;
    var focus: ?[]const u8 = null;
    var app_groups_start_index: usize = 1;

    if (args.len > 1) {
        var i: usize = 1;
        while (i < args.len) : (i += 1) {
            const arg = args[i];
            if (eql(u8, arg, "--listen")) {
                listen = true;
                app_groups_start_index = i + 1;
            } else if (eql(u8, arg, "--focus")) {
                if (i + 1 < args.len) {
                    focus = args[i + 1];
                    i += 1;
                    app_groups_start_index = i + 1;
                }
            }
        }
    }

    const app_groups = if (app_groups_start_index < args.len) args[app_groups_start_index..] else &[_][]const u8{};

    organizeWorkspaces(arena, app_groups, focus);

    if (listen) try hyprland.listenForEvents(arena, eventHandler, app_groups);
}

fn eventHandler(allocator: Allocator, line: []const u8, app_groups: []const []const u8) void {
    if (!std.mem.startsWith(u8, line, "monitoraddedv2") and !std.mem.startsWith(u8, line, "monitorremovedv2")) return;

    // Sleep for 200ms
    const timespec = std.posix.timespec{
        .sec = 0,
        .nsec = 200 * 1000 * 1000, // 200ms in nanoseconds
    };
    _ = std.os.linux.nanosleep(&timespec, null);
    organizeWorkspaces(allocator, app_groups, null);
}

fn organizeWorkspaces(arena: Allocator, app_groups: []const []const u8, focus: ?[]const u8) void {
    const active_window = hyprland.getActiveWindow(arena) catch |err| blk: {
        std.log.err("Failed to get active window: {any}", .{err});
        break :blk null;
    };

    const cursor_position = hyprland.getCursorPosition(arena) catch |err| blk: {
        std.log.err("Failed to get cursor position: {any}", .{err});
        break :blk null;
    };

    moveWorkspaces(arena) catch |err| {
        std.log.err("Failed to move workspaces: {any}", .{err});
        return;
    };

    moveApplications(arena, app_groups) catch |err| {
        std.log.err("Failed to move applications: {any}", .{err});
        return;
    };

    if (focus) |workspace| {
        const command = fmt(arena, "dispatch workspace {s}", .{workspace}) catch |err| {
            std.log.err("Failed to create focus command: {any}", .{err});
            return;
        };
        _ = hyprland.hyprlandCommand(arena, command) catch |err| {
            std.log.err("Failed to dispatch workspace {s}: {any}", .{ workspace, err });
        };
    } else if (active_window != null and cursor_position != null) {
        setMouseToActiveWindow(arena, active_window.?, cursor_position.?) catch |err| {
            std.log.err("Failed to set mouse to active window: {any}", .{err});
        };
    } else {
        _ = hyprland.hyprlandCommand(arena, "dispatch workspace 1") catch |err| {
            std.log.err("Failed to dispatch workspace 1: {any}", .{err});
        };
    }
}

fn moveWorkspaces(arena: Allocator) !void {
    const monitors = try hyprland.getMonitors(arena);

    std.sort.block(types.Monitor, monitors, {}, types.Monitor.monitorLessThan);
    for (monitors, 1..) |monitor, i| {
        const command = try fmt(arena, "dispatch workspace {d}", .{i});
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
