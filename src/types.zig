const std = @import("std");

pub const Monitor = struct {
    description: []const u8,
    id: i64,
    x: i64,
    y: i64,

    pub const CompareContext = struct {
        monitors: []const Monitor,
    };

    pub fn monitorLessThan(_: void, a: Monitor, b: Monitor) bool {
        return sort(a, b) == .lt;
    }

    pub fn sort(a: Monitor, b: Monitor) std.math.Order {
        if (a.x < b.x) return .lt;
        if (a.x > b.x) return .gt;
        if (a.y < b.y) return .lt;
        if (a.y > b.y) return .gt;
        return .eq;
    }
};

pub const Workspace = struct {
    id: i64,
    name: []u8,
};

pub const Client = struct {
    address: []const u8,
    class: []const u8,
    at: []f32,
    size: []f32,
    workspace: Workspace,
};

pub const CursorPosition = struct {
    x: f32,
    y: f32,
};
