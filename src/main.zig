const std = @import("std");
const hyprland = @import("hyprland.zig");
const types = @import("types.zig");

const AppError = error{
    OutOfMemory,
    FileSystemError,
};

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    defer std.process.argsFree(allocator, args);

    var app_groups_list = std.ArrayList([]const u8).init(allocator);
    defer app_groups_list.deinit();

    var listen = false;

    for (args[1..]) |arg| {
        if (std.mem.eql(u8, arg, "--listen")) {
            listen = true;
        } else {
            try app_groups_list.append(arg);
        }
    }

    const app_groups = try app_groups_list.toOwnedSlice();
    try organizeWorkspaces(allocator, app_groups);

    if (listen) try hyprland.listenForEvents(allocator, eventHandler, app_groups);
}

fn organizeWorkspaces(allocator: std.mem.Allocator, app_groups: []const []const u8) !void {
    const monitors = try hyprland.getMonitors(allocator);
    defer allocator.free(monitors);

    std.sort.block(types.Monitor, monitors, {}, types.Monitor.monitorLessThan);

    for (monitors, 1..) |monitor, i| {
        const command = try std.fmt.allocPrint(allocator, "dispatch moveworkspacetomonitor {d} {d}", .{ i, monitor.id });
        try hyprland.sendCommand(allocator, command);
    }

    const active_window = try hyprland.getActiveWindow(allocator);
    const cursor_position = try hyprland.getCursorPosition(allocator);

    for (app_groups, 1..) |app_group, i| {
        if (std.mem.eql(u8, app_group, "skip")) continue;

        var apps = std.mem.splitScalar(u8, app_group, ',');
        const clients = try hyprland.getClients(allocator);

        while (apps.next()) |app| {
            var command: ?[]u8 = null;
            for (clients) |client| {
                if (std.mem.indexOf(u8, client.class, app) == null) continue;

                command = try std.fmt.allocPrint(allocator, "dispatch movetoworkspacesilent {d},address:{s}", .{ i, client.address });
                break;
            }

            const command_result = command orelse try std.fmt.allocPrint(allocator, "dispatch exec [workspace {d} silent] {s}", .{ i, app });
            try hyprland.sendCommand(allocator, command_result);
        }
    }

    if (active_window) |active_window_result| {
        const command = try std.fmt.allocPrint(allocator, "dispatch focuswindow address:{s}", .{active_window_result.address});
        try hyprland.sendCommand(allocator, command);

        const active_window_new = try hyprland.getActiveWindow(allocator);
        if (active_window_new) |active_window_new_result| {
            const relative_x: i32 = @intFromFloat(@as(f32, @floatFromInt(cursor_position.x - active_window_result.at[0])) * (@as(f32, @floatFromInt(active_window_new_result.size[0])) / @as(f32, @floatFromInt(active_window_result.size[0]))));
            const relative_y: i32 = @intFromFloat(@as(f32, @floatFromInt(cursor_position.y - active_window_result.at[1])) * (@as(f32, @floatFromInt(active_window_new_result.size[1])) / @as(f32, @floatFromInt(active_window_result.size[1]))));

            if (relative_x <= active_window_new_result.size[0] and relative_y <= active_window_new_result.size[1]) {
                const x: i32 = relative_x + active_window_new_result.at[0];
                const y: i32 = relative_y + active_window_new_result.at[1];

                const mouse_command = try std.fmt.allocPrint(allocator, "dispatch movecursor {d} {d}", .{ x, y });
                try hyprland.sendCommand(allocator, mouse_command);
            }
        }
    }
}

fn eventHandler(allocator: std.mem.Allocator, line: []const u8, app_groups: []const []const u8) anyerror!void {
    if (std.mem.startsWith(u8, line, "monitoraddedv2") or std.mem.startsWith(u8, line, "monitorremovedv2")) {
        std.time.sleep(200 * 1000 * 1000); // 200ms
        try organizeWorkspaces(allocator, app_groups);
    }
}
