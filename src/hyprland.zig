const std = @import("std");
const types = @import("types.zig");

const HyprlandError = error{
    MissingEnvVar,
    SocketConnectFailed,
    CommandFailed,
    JsonParseFailed,
};

fn getHyprlandEnv(arena: std.mem.Allocator, name: []const u8) ![]const u8 {
    return std.process.getEnvVarOwned(arena, name) catch |err| {
        std.log.err("missing required environment variable: {s}", .{name});
        return err;
    };
}

fn getSocketPath(arena: std.mem.Allocator, socket_name: []const u8) ![]const u8 {
    const xdg_runtime_dir = try getHyprlandEnv(arena, "XDG_RUNTIME_DIR");
    const hyprland_instance_sig = try getHyprlandEnv(arena, "HYPRLAND_INSTANCE_SIGNATURE");

    return std.fmt.allocPrint(arena, "{s}/hypr/{s}/{s}", .{
        xdg_runtime_dir,
        hyprland_instance_sig,
        socket_name,
    });
}

pub fn hyprlandCommand(arena: std.mem.Allocator, command: []const u8) ![]u8 {
    const socket_path = try getSocketPath(arena, ".socket.sock");

    const stream = try std.net.connectUnixSocket(socket_path);
    defer stream.close();

    try stream.writer().writeAll(command);

    const result = try stream.reader().readAllAlloc(arena, 1024 * 16); // 16kB limit

    return result;
}

pub fn getMonitors(arena: std.mem.Allocator) ![]types.Monitor {
    const json_data = try hyprlandCommand(arena, "j/monitors");

    const result = std.json.parseFromSlice([]types.Monitor, arena, json_data, .{ .ignore_unknown_fields = true }) catch |err| {
        std.log.err("failed to parse monitors JSON: {any}", .{err});
        return HyprlandError.JsonParseFailed;
    };
    return result.value;
}

pub fn getClients(arena: std.mem.Allocator) ![]types.Client {
    const json_data = try hyprlandCommand(arena, "j/clients");

    const result = std.json.parseFromSlice([]types.Client, arena, json_data, .{ .ignore_unknown_fields = true }) catch |err| {
        std.log.err("failed to parse clients JSON: {any}", .{err});
        return HyprlandError.JsonParseFailed;
    };
    
    return result.value;
}

pub fn getActiveWindow(arena: std.mem.Allocator) !?types.Client {
    const json_data = try hyprlandCommand(arena, "j/activewindow");

    if(std.mem.eql(u8, json_data, "{}")) return null;

    const result = std.json.parseFromSlice(?types.Client, arena, json_data, .{ .ignore_unknown_fields = true }) catch |err| {
        std.log.err("failed to parse clients JSON: {any}", .{err});
        return HyprlandError.JsonParseFailed;
    };

    return result.value;
}

pub fn getCursorPosition(arena: std.mem.Allocator) !types.CursorPosition {
    const json_data = try hyprlandCommand(arena, "j/cursorpos");

    const result = std.json.parseFromSlice(types.CursorPosition, arena, json_data, .{ .ignore_unknown_fields = true }) catch |err| {
        std.log.err("failed to parse clients JSON: {any}", .{err});
        return HyprlandError.JsonParseFailed;
    };

    return result.value;
}

pub fn listenForEvents(arena: std.mem.Allocator, handler: *const fn (arena: std.mem.Allocator, line: []const u8, app_groups: []const []const u8) void, app_groups: []const []const u8) !void {
    const socket_path = try getSocketPath(arena, ".socket2.sock");

    const stream = try std.net.connectUnixSocket(socket_path);
    defer stream.close();

    var buf_reader = std.io.bufferedReader(stream.reader());
    const reader = buf_reader.reader();

    std.log.info("listening for hyprland events on {s}", .{socket_path});
    var buffer: [1024]u8 = undefined;

    while (try reader.readUntilDelimiterOrEof(&buffer, '\n')) |line| {
        var arena_allocator = std.heap.ArenaAllocator.init(std.heap.page_allocator);
        defer arena_allocator.deinit();
        const event_arena = arena_allocator.allocator();

        handler(event_arena, line, app_groups);
    }
}
