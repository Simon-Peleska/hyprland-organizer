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

    var listen = false;
    var app_groups_list = std.ArrayList([]const u8).init(allocator);
    defer app_groups_list.deinit();

    for (args[1..]) |arg| {
        if (std.mem.eql(u8, arg, "--listen")) {
            listen = true;
        } else {
            try app_groups_list.append(arg);
        }
    }
    const app_groups = try app_groups_list.toOwnedSlice();

    try organizeWorkspaces(allocator, app_groups);

    if (listen) {
        try hyprland.listenForEvents(allocator, eventHandler, app_groups);
    }
}

fn organizeWorkspaces(allocator: std.mem.Allocator, app_groups: []const []const u8) !void {

    const monitors = try hyprland.getMonitors(allocator);

    std.sort.block(types.Monitor, monitors, {}, types.Monitor.monitorLessThan);

    for (monitors, 1..) |monitor, i| {
        try hyprland.sendCommand(allocator, try std.fmt.allocPrint(allocator, "dispatch moveworkspacetomonitor {d} {d}", .{ i, monitor.id }));
    }

    for (app_groups, 1..) |app_group, i| {
        if (std.mem.eql(u8, app_group, "skip")) {
            continue;
        }

        var apps = std.mem.splitScalar(u8, app_group, ',');
        const clients = try hyprland.getClients(allocator);
        const active_window = try hyprland.getActiveWindow(allocator);

        while (apps.next()) |app| {
            var client_found = false;
            for (clients) |client| {
                if (std.mem.indexOf(u8, client.class, app) != null) {
                    try hyprland.sendCommand(allocator, try std.fmt.allocPrint(allocator, "dispatch movetoworkspacesilent {d},address:{s}", .{ i, client.address }));
                    client_found = true;
                    break;
                }
            }

            if (!client_found) {
                try hyprland.sendCommand(allocator, try std.fmt.allocPrint(allocator, "dispatch exec [workspace {d} silent] {s}", .{ i, app }));
            }
        }

        if (active_window) |active_window_result| {
            try hyprland.sendCommand(allocator, try std.fmt.allocPrint(allocator, "dispatch focuswindow address:{s}", .{active_window_result.address}));
        }
    }
}

fn eventHandler(allocator: std.mem.Allocator, line: []const u8, app_groups: []const []const u8) anyerror!void {
    if (std.mem.startsWith(u8, line, "monitoraddedv2") or std.mem.startsWith(u8, line, "monitorremovedv2")) {
        std.time.sleep(200 * 1000 * 1000); // 200ms
        try organizeWorkspaces(allocator, app_groups);
    }
}

