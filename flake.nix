{
  description = "Hyprland workspace organizer";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      nixpkgs,
      flake-utils,
      ...
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "hyprland-organizer";
          version = "0.1.0";
          src = pkgs.lib.cleanSource ./.;
          vendorHash = null;
        };

        devShells.default = pkgs.mkShell {
          buildInputs = [ pkgs.go ];
        };
      }
    );
}
