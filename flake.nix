{
  description = "Bootstrap Operator — Kubernetes operator for managing git repos, registries, and image builds via CRDs";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-25.11";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        version = "0.1.0";
        imageRepo = "registry.cia.net/operators/bootstrap-operator";

        # ── Output 1: static Go binary ──────────────────────────────────
        bootstrap-operator = pkgs.buildGoModule {
          pname = "bootstrap-operator";
          inherit version;
          src = ./.;
          vendorHash = null; # update after first build

          env.CGO_ENABLED = 0;
          subPackages = [ "cmd" ];
          ldflags = [
            "-s"
            "-w"
          ];

          postInstall = ''
            mv $out/bin/cmd $out/bin/manager
          '';
        };

        # ── Output 2: OCI image ─────────────────────────────────────────
        image = pkgs.dockerTools.buildLayeredImage {
          name = imageRepo;
          tag = version;
          contents = [
            bootstrap-operator
            pkgs.cacert
          ];
          config = {
            Entrypoint = [ "${bootstrap-operator}/bin/manager" ];
            User = "65532:65532";
          };
        };

        # ── Output 3: K8s manifests ─────────────────────────────────────
        manifests = pkgs.runCommand "bootstrap-operator-manifests" { } ''
          cat ${./config/crds.yaml} > $out
          echo "---" >> $out
          cat ${./config/rbac.yaml} >> $out
          echo "---" >> $out
          ${pkgs.gnused}/bin/sed \
            's|registry.cia.net/operators/bootstrap-operator:latest|${imageRepo}:${version}|' \
            ${./config/deployment.yaml} >> $out
        '';
      in
      {
        packages = {
          default = bootstrap-operator;
          inherit image manifests;
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            controller-tools
            golangci-lint
          ];
        };
      }
    );
}
