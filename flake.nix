{
  description = "Run main.go in Kubernetes with one command, also port-forward your app into Kubernetes";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    devenv.url = "github:cachix/devenv";
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [
        inputs.devenv.flakeModule
      ];

      systems = [ "x86_64-linux" "x86_64-darwin" "aarch64-darwin" ];

      perSystem = { config, self', inputs', pkgs, system, ... }: rec {
        devenv.shells = {
          default = {
            languages = {
              go.enable = true;
            };

            packages = with pkgs; [
              gnumake

              golangci-lint
              goreleaser

              kind
              kubectl
              kustomize
            ];

            scripts = {
              versions.exec = ''
                go version
                golangci-lint version
                echo "goreleaser $(goreleaser --version | sed -n '9p' | cut -d ' ' -f 5)"
                kind version
                kubectl version --client
                echo kustomize $(kustomize version --short)
              '';
            };

            enterShell = ''
              versions
            '';

            # https://github.com/cachix/devenv/issues/528#issuecomment-1556108767
            containers = pkgs.lib.mkForce { };
          };

          ci = devenv.shells.default;
        };
      };
    };
}
