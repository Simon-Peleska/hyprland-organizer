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
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    defer std.process.argsFree(allocator, args);

    var app_groups_list = std.ArrayList([]const u8).init(allocator);
    defer app_groups_list.deinit();

    var listen = false;

    for (args[1..]) |arg| {
        if (eql(u8, arg, "--listen")) {
            listen = true;
        } else {
            try app_groups_list.append(arg);
        }
    }

    const app_groups = try app_groups_list.toOwnedSlice();
    try organizeWorkspaces(allocator, app_groups);

    if (listen) try hyprland.listenForEvents(allocator, eventHandler, app_groups);
}

fn organizeWorkspaces(allocator: Allocator, app_groups: []const []const u8) !void {
    const monitors = try hyprland.getMonitors(allocator);
    defer allocator.free(monitors);

    std.sort.block(types.Monitor, monitors, {}, types.Monitor.monitorLessThan);
    var commands = std.ArrayList(u8).init(allocator);
    var initial_commands = std.ArrayList(u8).init(allocator);
    for (monitors, 1..) |monitor, i| {
        const command = try fmt(allocator, "dispatch moveworkspacetomonitor {d} {d};", .{ i, monitor.id });
        try commands.appendSlice(command);
    }

    const active_window = try hyprland.getActiveWindow(allocator);
    const cursor_position = try hyprland.getCursorPosition(allocator);

    for (app_groups, 1..) |app_group, i| {
        if (eql(u8, app_group, "skip")) continue;

        var apps = std.mem.splitScalar(u8, app_group, ',');
        const clients = try hyprland.getClients(allocator);

        while (apps.next()) |app| {
            var command: ?[]u8 = null;
            for (clients) |client| {
                if (std.mem.indexOf(u8, client.class, app) == null) continue;
                if (client.workspace.id == i) break;
                
                const init_command_result = try fmt(allocator, "dispatch movetoworkspacesilent special:{s},address:{s};", .{ client.address, client.address });
                try initial_commands.appendSlice(init_command_result);

                command = try fmt(allocator, "dispatch movetoworkspacesilent {d},address:{s};", .{ i, client.address });
                break;
            }

            const command_result = command orelse try fmt(allocator, "dispatch exec [workspace {d} silent] {s};", .{ i, app });
            try commands.appendSlice(command_result);
       }
    }

    if (active_window) |active_window_result| {
        const command = try fmt(allocator, "dispatch focuswindow address:{s};", .{active_window_result.address});
        try commands.appendSlice(command);
    }

    const result = try hyprland.hyprlandCommand(allocator, try fmt(allocator, "[[BATCH]]{s}{s}", .{initial_commands.items, commands.items}));
    defer allocator.free(result);

    if (active_window) |active_window_result| {
        const active_window_new = try hyprland.getActiveWindow(allocator);
        if (active_window_new) |active_window_new_result| {
            const relative_x: i32 = @intFromFloat(@as(f32, @floatFromInt(cursor_position.x - active_window_result.at[0])) * (@as(f32, @floatFromInt(active_window_new_result.size[0])) / @as(f32, @floatFromInt(active_window_result.size[0]))));
            const relative_y: i32 = @intFromFloat(@as(f32, @floatFromInt(cursor_position.y - active_window_result.at[1])) * (@as(f32, @floatFromInt(active_window_new_result.size[1])) / @as(f32, @floatFromInt(active_window_result.size[1]))));

            if (relative_x <= active_window_new_result.size[0] and relative_y <= active_window_new_result.size[1]) {
                const x: i32 = relative_x + active_window_new_result.at[0];
                const y: i32 = relative_y + active_window_new_result.at[1];

                const mouse_command = try fmt(allocator, "dispatch movecursor {d} {d}", .{ x, y });
                const result2 = try hyprland.hyprlandCommand(allocator, mouse_command);
                defer allocator.free(result2);
            }
        }
    }
}

fn eventHandler(allocator: Allocator, line: []const u8, app_groups: []const []const u8) anyerror!void {
    if (std.mem.startsWith(u8, line, "monitoraddedv2") or std.mem.startsWith(u8, line, "monitorremovedv2")) {
        std.time.sleep(200 * 1000 * 1000); // 200ms
        try organizeWorkspaces(allocator, app_groups);
    }
}
