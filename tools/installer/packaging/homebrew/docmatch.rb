# Fórmula do Homebrew para o docmatch 0.1.2, gerada pelo tools/installer/release.sh a partir
# do SHA256SUMS.txt da versão (molde em tools/installer/packaging/templates/docmatch.rb). Os
# binários são os mesmos do install.sh, nas cópias fixas da versão.
class Docmatch < Formula
  desc "Instalador e linha de comando da docmatch: concilia documentos nesta máquina"
  homepage "https://docmatch.com.br"
  version "0.1.2"
  license "AGPL-3.0-only"

  on_macos do
    on_arm do
      url "https://docmatch.com.br/install/v0.1.2/docmatch-darwin-arm64.bin"
      sha256 "57ec0cad2f3369403b9e411b6c7fca68189e232c3dab7ffec9c212e79efe986f"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.1.2/docmatch-darwin-amd64.bin"
      sha256 "700b7a52470734818cbe2f83bd158ca24788a8bc5a95857d96ef818d37da3624"
    end
  end

  on_linux do
    on_arm do
      url "https://docmatch.com.br/install/v0.1.2/docmatch-linux-arm64.bin"
      sha256 "4dfa8588e8faee8cd7598bd2a0dcfb0ca6aab10a7de7e7289bdb241834a8a76c"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.1.2/docmatch-linux-amd64.bin"
      sha256 "af8a5f02e8c68357c10d719e5fc35737666880947114e0f811a540d82d6ec219"
    end
  end

  def install
    bin.install Dir["docmatch-*.bin"].first => "docmatch"
  end

  test do
    assert_equal "docmatch #{version}", shell_output("#{bin}/docmatch --version").strip
  end
end
