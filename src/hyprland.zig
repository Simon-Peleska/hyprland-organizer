const std = @import("std");
const types = @import("types.zig");

const HyprlandError = error{
    MissingEnvVar,
    SocketConnectFailed,
    CommandFailed,
    JsonParseFailed,
};

fn getHyprlandEnv(allocator: std.mem.Allocator, name: []const u8) ![]const u8 {
    return std.process.getEnvVarOwned(allocator, name) catch |err| {
        std.log.err("missing required environment variable: {s}", .{name});
        return err;
    };
}

fn getSocketPath(allocator: std.mem.Allocator, socket_name: []const u8) ![]const u8 {
    const xdg_runtime_dir = try getHyprlandEnv(allocator, "XDG_RUNTIME_DIR");
    defer allocator.free(xdg_runtime_dir);

    const hyprland_instance_sig = try getHyprlandEnv(allocator, "HYPRLAND_INSTANCE_SIGNATURE");
    defer allocator.free(hyprland_instance_sig);

    return std.fmt.allocPrint(allocator, "{s}/hypr/{s}/{s}", .{
        xdg_runtime_dir,
        hyprland_instance_sig,
        socket_name,
    });
}

fn hyprlandCommand(allocator: std.mem.Allocator, command: []const u8) ![]u8 {
    const socket_path = try getSocketPath(allocator, ".socket.sock");
    defer allocator.free(socket_path);

    const stream = try std.net.connectUnixSocket(socket_path);
    defer stream.close();

    try stream.writer().writeAll(command);

    return stream.reader().readAllAlloc(allocator, 1024 * 1024); // 1MB limit
}

pub fn sendCommand(allocator: std.mem.Allocator, commands: []const u8) !void {
    if (commands.len == 0) return;
    const batch_command = try std.fmt.allocPrint(allocator, "{s}", .{commands});
    defer allocator.free(batch_command);
    const output = try hyprlandCommand(allocator, batch_command);

    defer allocator.free(output); // free the reply, we don't care about it.
}

pub fn getMonitors(allocator: std.mem.Allocator) ![]types.Monitor {
    const json_data = try hyprlandCommand(allocator, "j/monitors");

    const result = std.json.parseFromSlice([]types.Monitor, allocator, json_data, .{ .ignore_unknown_fields = true }) catch |err| {
        std.log.err("failed to parse monitors JSON: {any}", .{err});
        return HyprlandError.JsonParseFailed;
    };
    return result.value;
}

pub fn getClients(allocator: std.mem.Allocator) ![]types.Client {
    const json_data = try hyprlandCommand(allocator, "j/clients");
    // defer allocator.free(json_data);

    const result = std.json.parseFromSlice([]types.Client, allocator, json_data, .{ .ignore_unknown_fields = true }) catch |err| {
        std.log.err("failed to parse clients JSON: {any}", .{err});
        return HyprlandError.JsonParseFailed;
    };
    return result.value;
}

pub fn listenForEvents(allocator: std.mem.Allocator, handler: *const fn (line: []const u8) anyerror!void) !void {
    const socket_path = try getSocketPath(allocator, ".socket2.sock");
    defer allocator.free(socket_path);

    const stream = try std.net.connectUnixSocket(socket_path);
    defer stream.close();

    var buf_reader = std.io.bufferedReader(stream.reader());
    const reader = buf_reader.reader();

    std.log.info("listening for hyprland events on {s}", .{socket_path});
    var buffer: [1024]u8 = undefined;
    while (try reader.readUntilDelimiterOrEof(&buffer, '\n')) |line| {
        try handler(line);
    }
}
