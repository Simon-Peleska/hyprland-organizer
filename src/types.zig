const std = @import("std");

/// Represents a monitor as reported by `hyprctl monitors -j`.
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

/// Represents a client (window) as reported by `hyprctl clients -j`.
pub const Client = struct {
    address: []const u8,
    class: []const u8,
};
