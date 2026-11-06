# Fórmula do Homebrew para o docmatch 0.1.1, gerada pelo tools/installer/release.sh a partir
# do SHA256SUMS.txt da versão (molde em tools/installer/packaging/templates/docmatch.rb). Os
# binários são os mesmos do install.sh, nas cópias fixas da versão.
class Docmatch < Formula
  desc "Instalador e linha de comando da docmatch: concilia documentos nesta máquina"
  homepage "https://docmatch.com.br"
  version "0.1.1"
  license "AGPL-3.0-only"

  on_macos do
    on_arm do
      url "https://docmatch.com.br/install/v0.1.1/docmatch-darwin-arm64.bin"
      sha256 "0b0ab52b67b8544df59247d680a72c4ca7f4b09b040843330ccb89342903b222"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.1.1/docmatch-darwin-amd64.bin"
      sha256 "107e7f01dc8754b85c46c5886677efb663309d00696bbd0b42432ee7a8d9e67c"
    end
  end

  on_linux do
    on_arm do
      url "https://docmatch.com.br/install/v0.1.1/docmatch-linux-arm64.bin"
      sha256 "46109857b76a1cbb782a4581bc30e4136bd695ba3c62fa11db57d17f269b5048"
    end
    on_intel do
      url "https://docmatch.com.br/install/v0.1.1/docmatch-linux-amd64.bin"
      sha256 "93e449acddf42e54c4624e424d43b9fb7bc72d9edd1f2ce2d30a3b42e213facb"
    end
  end

  def install
    bin.install Dir["docmatch-*.bin"].first => "docmatch"
  end

  test do
    assert_equal "docmatch #{version}", shell_output("#{bin}/docmatch --version").strip
  end
end
