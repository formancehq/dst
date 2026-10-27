{
  description = "A Nix-flake-based Go 1.27.3 development environment";

  inputs = {
    nixpkgs.url = "https://flakehub.com/f/NixOS/nixpkgs/0.1.*.tar.gz";

    # Use the stable upstream compiler and matching module builder.
    nixpkgs-go = {
      url = "https://api.flakehub.com/f/pinned/NixOS/nixpkgs/0.2605.1016949%2Brev-1f607aa79b6356ed45d3cd3c8079c196e79efa2c/01a17424-ae2d-7b20-83c4-cf14527294c3/source.tar.gz";
      flake = false;
    };

    # Keep golangci-lint 2.15.0; stable currently provides 2.12.2.
    go-tooling = {
      url = "github:NixOS/nixpkgs/8cec5ef1131078ea76689e84c362b0ed6a1fb6a1";
      flake = false;
    };

    nur = {
      url = "github:nix-community/NUR";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nixpkgs-go, nur, go-tooling }:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      forEachSupportedSystem = f:
        nixpkgs.lib.genAttrs supportedSystems (system:
          let
            pkgs = import nixpkgs {
              inherit system;
              overlays = [ self.overlays.default nur.overlays.default ];
              config.allowUnfree = true;
            };
          in
          f { pkgs = pkgs; system = system; }
        );

      speakeasyVersion = "1.563.0";
      speakeasyPlatforms = {
        "x86_64-linux"   = "linux_amd64";
        "aarch64-linux"  = "linux_arm64";
        "x86_64-darwin"  = "darwin_amd64";
        "aarch64-darwin" = "darwin_arm64";
      };
      speakeasyHashes = {
        "x86_64-linux"   = "632559a6bdc765ef42b81b8404fd0a3e5023919a0bb70ff7e40a8cc259545afd";
        "aarch64-linux"  = "c74c502df3a05a2d69e6b282886df23354a319d0510d2c1a21fcc124b7ad00ef";
        "x86_64-darwin"  = "8814be1fdd4eaf6dcc7fb251ede5693e1d3d4c8e03986f8d37bfd59e049698b9";
        "aarch64-darwin" = "12c20fa485de4725c9730cb2e8936cab4b0961d0a956e9f4c45534371f2a6148";
      };

    in
    {
      overlays.default = final: prev:
        let
          goPkgs = import nixpkgs-go {
            inherit (prev.stdenv.hostPlatform) system;
          };
        in {
          go = goPkgs.go_1_27;
          buildGo127Module = goPkgs.buildGo127Module;
          mockgen = prev.mockgen.override { buildGoModule = goPkgs.buildGo127Module; };
          golangci-lint = goPkgs.callPackage "${go-tooling}/pkgs/by-name/go/golangci-lint/package.nix" { };
        };

      packages = forEachSupportedSystem ({ pkgs, system }: {});

      defaultPackage.x86_64-linux   = self.packages.x86_64-linux.speakeasy;
      defaultPackage.aarch64-linux  = self.packages.aarch64-linux.speakeasy;
      defaultPackage.x86_64-darwin  = self.packages.x86_64-darwin.speakeasy;
      defaultPackage.aarch64-darwin = self.packages.aarch64-darwin.speakeasy;

      devShells = forEachSupportedSystem ({ pkgs, system }:
        {
          default = pkgs.mkShell {
            GOTOOLCHAIN = "local";
            GOROOT = "${pkgs.go}/share/go";
            packages = with pkgs; [
              go
              gotools
              go-tools
              golangci-lint
              ginkgo
              yq-go
              jq
              pkgs.nur.repos.goreleaser.goreleaser-pro
              mockgen
              gomarkdoc
              jdk11
              just
              nodejs_22
              goperf
              protobuf_27
              protoc-gen-go-grpc
              protoc-gen-go
            ];
          };
        }
      );
    };
}
