{
  description = "A Nix-flake-based Go 1.27.3 development environment";

  inputs = {
    nixpkgs.url = "https://flakehub.com/f/NixOS/nixpkgs/0.1.*.tar.gz";

    go-tooling = {
      url = "github:NixOS/nixpkgs/8cec5ef1131078ea76689e84c362b0ed6a1fb6a1";
      flake = false;
    };

    nur = {
      url = "github:nix-community/NUR";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nur, go-tooling }:
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
      overlays.default = final: prev: {
        go = final.callPackage "${go-tooling}/pkgs/development/compilers/go/1.27.nix" {
          buildGo127Module = final.buildGo127Module;
        };
        buildGo127Module = prev.buildGoModule.override { go = final.go; };
        mockgen = prev.mockgen.override { buildGoModule = final.buildGo127Module; };
        golangci-lint = let
          go127 = final.callPackage "${go-tooling}/pkgs/development/compilers/go/1.27.nix" {
            buildGo127Module = build127;
          };
          build127 = prev.buildGoModule.override { go = go127; };
        in final.callPackage "${go-tooling}/pkgs/by-name/go/golangci-lint/package.nix" {
          buildGo127Module = build127;
        };
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
