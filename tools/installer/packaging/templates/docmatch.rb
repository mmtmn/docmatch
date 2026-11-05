# Fórmula do Homebrew para o docmatch __VERSION__, gerada pelo tools/installer/release.sh a partir
# do SHA256SUMS.txt da versão (molde em tools/installer/packaging/templates/docmatch.rb). Os
# binários são os mesmos do install.sh, nas cópias fixas da versão.
class Docmatch < Formula
  desc "Instalador e linha de comando da docmatch: concilia documentos nesta máquina"
  homepage "__HOMEPAGE__"
  version "__VERSION__"
  license "AGPL-3.0-only"

  on_macos do
    on_arm do
      url "__BASE__/docmatch-darwin-arm64.bin"
      sha256 "__SHA_DARWIN_ARM64__"
    end
    on_intel do
      url "__BASE__/docmatch-darwin-amd64.bin"
      sha256 "__SHA_DARWIN_AMD64__"
    end
  end

  on_linux do
    on_arm do
      url "__BASE__/docmatch-linux-arm64.bin"
      sha256 "__SHA_LINUX_ARM64__"
    end
    on_intel do
      url "__BASE__/docmatch-linux-amd64.bin"
      sha256 "__SHA_LINUX_AMD64__"
    end
  end

  def install
    bin.install Dir["docmatch-*.bin"].first => "docmatch"
  end

  test do
    assert_equal "docmatch #{version}", shell_output("#{bin}/docmatch --version").strip
  end
end
