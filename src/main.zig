const std = @import("std");
const hyprland = @import("hyprland.zig");
const types = @import("types.zig");

const AppError = error{
    OutOfMemory,
    FileSystemError,
};

fn organizeWorkspaces(app_groups: []const []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    if (app_groups.len == 0) {
        const monitors = try hyprland.getMonitors(allocator);
        std.sort.block(types.Monitor, monitors, {}, types.Monitor.monitorLessThan);

        var workspace: u32 = 1;
        for (monitors) |monitor| {
            try hyprland.sendCommand(allocator, try std.fmt.allocPrint(allocator, "dispatch moveworkspacetomonitor {d} {d}", .{ workspace, monitor.id }));
            workspace += 1;
        }
    }


    var workspace: u32 = 1;
    const effective_app_groups = if (app_groups.len > 0) app_groups else &[_][]const u8{ "ghostty", "chromium" };

    for (effective_app_groups) |app_group| {
        if (std.mem.eql(u8, app_group, "skip")) {
            workspace += 1;
            continue;
        }

        var apps = std.mem.splitScalar(u8, app_group, ',');
        const clients = try hyprland.getClients(allocator);
        while (apps.next()) |app| {
            var client_found = false;
            for (clients) |client| {
                if (std.mem.indexOf(u8, client.class, app) != null) {
                    try hyprland.sendCommand(allocator, try std.fmt.allocPrint(allocator, "dispatch movetoworkspacesilent {d},address:{s}", .{ workspace, client.address }));
                    client_found = true;
                    break;
                }
            }

            if (!client_found) {
                try hyprland.sendCommand(allocator, try std.fmt.allocPrint(allocator, "dispatch exec [workspace {d} silent] {s}", .{ workspace, app }));
            }

        }

        workspace += 1;
    }
}

fn eventHandler(line: []const u8) anyerror!void {
    if (std.mem.startsWith(u8, line, "monitoraddedv2") or std.mem.startsWith(u8, line, "monitorremovedv2")) {
        std.time.sleep(200 * 1000 * 1000); // 200ms
        try organizeWorkspaces(&.{});
    }
}

pub fn main() !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const args = try std.process.argsAlloc(allocator);
    defer std.process.argsFree(allocator, args);

    if (args.len > 1) {
        try organizeWorkspaces(args[1..]);

        if (std.mem.eql(u8, args[1], "--listen")) {
            try hyprland.listenForEvents(allocator, eventHandler);
        }
    } else {
        try organizeWorkspaces(&.{});
    }
}
