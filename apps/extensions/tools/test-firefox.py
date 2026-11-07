"""Local acceptance in an isolated Firefox profile; no user browser preferences change."""

import json, uuid, pathlib, os
from urllib.parse import urlparse
from selenium import webdriver
from selenium.webdriver.firefox.options import Options
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.firefox.service import Service

root = pathlib.Path(__file__).resolve().parents[3]
config = json.loads((root / "apps/extensions/local.config.json").read_text())
texts = json.loads((root / "packages/locales/pt-BR.json").read_text())


def label(group, key):
    return texts[group][key]


def xpath_literal(value):
    return json.dumps(value, ensure_ascii=False)


for origin in (config["apiURL"], config["siteURL"]):
    assert urlparse(origin).hostname in ("localhost", "127.0.0.1"), "local server only"
artifacts = root / ".cache/extensions-e2e/firefox"
artifacts.mkdir(parents=True, exist_ok=True)
extension_id = config["firefox"]["id"]
hostname = str(uuid.uuid4())
options = Options()
options.binary_location = os.environ.get(
    "FIREFOX_BINARY", "/Applications/Firefox.app/Contents/MacOS/firefox"
)
options.add_argument("-headless")
options.set_preference(
    "extensions.webextensions.uuids", json.dumps({extension_id: hostname})
)
options.set_preference("browser.download.folderList", 2)
options.set_preference("browser.download.dir", str(artifacts))
options.set_preference(
    "browser.helperApps.neverAsk.saveToDisk",
    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet,application/pdf,text/csv",
)
service = Service(
    log_output=str(artifacts / "geckodriver.log"),
    service_args=["--allow-system-access"],
    env={**os.environ, "MOZ_REMOTE_ALLOW_SYSTEM_ACCESS": "1"},
)
driver = webdriver.Firefox(options=options, service=service)
try:
    driver.set_script_timeout(20)
    installed = driver.install_web_extension(
        str(root / "apps/extensions/dist/local/firefox"), permanent=False
    )
    assert installed.id == extension_id
    driver.set_context("chrome")
    target = driver.execute_script(
        'return WebExtensionPolicy.getByID(arguments[0]).getURL("workspace.html")',
        extension_id,
    )
    driver.execute_script(
        "gBrowser.selectedTab = gBrowser.addTrustedTab(arguments[0]);", target
    )
    driver.set_context("content")
    driver.switch_to.window(driver.window_handles[-1])
    result = driver.execute_script(
        'return {redirect:browser.identity.getRedirectURL("callback"), id:browser.runtime.id, title:document.title}'
    )

    assert result["redirect"] == config["firefox"]["redirectURI"]
    wait = WebDriverWait(driver, 30)
    main = driver.current_window_handle
    wait.until(
        EC.element_to_be_clickable((By.XPATH, "//button[normalize-space()='Entrar']"))
    ).click()

    def auth_handle(d):
        for handle in d.window_handles:
            d.switch_to.window(handle)
            if d.current_url.startswith(config["siteURL"] + "/extensoes/autorizar"):
                return handle
        return False

    auth = wait.until(auth_handle)
    wait.until(
        EC.element_to_be_clickable((By.XPATH, "//button[normalize-space()='Entrar']"))
    ).click()
    wait.until(
        EC.element_to_be_clickable(
            (By.XPATH, "//button[contains(.,'Entrar sem conta')]")
        )
    ).click()
    wait.until(
        EC.element_to_be_clickable(
            (By.XPATH, "//button[normalize-space()='Conectar extensão']")
        )
    ).click()
    driver.switch_to.window(main)
    wait.until(
        EC.presence_of_element_located((By.CSS_SELECTOR, "select[aria-label=Conta]"))
    )

    def click(text):
        wait.until(
            EC.element_to_be_clickable(
                (
                    By.XPATH,
                    "//button[normalize-space()="
                    + json.dumps(text, ensure_ascii=False)
                    + "]",
                )
            )
        ).click()

    name = wait.until(
        EC.presence_of_element_located((By.CSS_SELECTOR, "input[id=project-name]"))
    )
    name.send_keys("Fechamento pela extensão Firefox")
    click(label("projects", "new"))
    wait.until(lambda d: "project=" in d.current_url)
    project_url = driver.current_url
    files = [
        root / "testdata/golden/vantara-twin" / file
        for file in [
            "01_contas_a_receber_protheus_jul2026.csv",
            "03_extrato_bancario_jul2026.csv",
        ]
    ]
    wait.until(
        EC.presence_of_element_located((By.CSS_SELECTOR, "input[type=file][multiple]"))
    ).send_keys("\n".join(map(str, files)))
    WebDriverWait(driver, 60).until(
        EC.presence_of_element_located(
            (
                By.XPATH,
                "//*[text()=" + xpath_literal(label("projects", "upload_saved")) + "]",
            )
        )
    )
    click(label("projects", "reconcile"))
    click(label("app", "confirm_start"))
    wait.until(lambda d: "run=" in d.current_url)
    results_url = driver.current_url
    # Close and reopen the foreground without losing the server-side run.
    driver.close()
    driver.switch_to.window(driver.window_handles[0])
    driver.set_context("chrome")
    driver.execute_script(
        "gBrowser.selectedTab = gBrowser.addTrustedTab(arguments[0]);", results_url
    )
    driver.set_context("content")
    driver.switch_to.window(driver.window_handles[-1])
    WebDriverWait(driver, 120).until(
        EC.presence_of_element_located(
            (
                By.XPATH,
                "//h1[normalize-space()="
                + xpath_literal(label("results", "document_ready"))
                + "]",
            )
        )
    )
    before = set(artifacts.glob("*.xlsx"))
    click(label("results", "download_corrected"))

    def downloaded(_):
        new = set(artifacts.glob("*.xlsx")) - before
        return next(
            (
                p
                for p in new
                if p.stat().st_size > 1000
                and not pathlib.Path(str(p) + ".part").exists()
            ),
            False,
        )

    exported = wait.until(downloaded)
    wait.until(
        EC.element_to_be_clickable(
            (By.XPATH, "//summary[contains(.,'Consultar todos os dados e achados')]")
        )
    ).click()
    rows = wait.until(
        EC.presence_of_all_elements_located(
            (By.CSS_SELECTOR, "[data-testid=finding-row]")
        )
    )
    rows[0].find_element(By.TAG_NAME, "button").click()
    wait.until(EC.presence_of_element_located((By.CSS_SELECTOR, "dialog[open]")))
    driver.find_element(By.CSS_SELECTOR, "dialog[open] button[aria-label]").click()
    driver.get(project_url)
    click(label("projects", "reconcile"))
    wait.until(lambda d: "run=" in d.current_url)
    assert (
        driver.current_url == results_url
    ), "unchanged project must reopen the same run"
    driver.get(project_url)
    wait.until(
        EC.presence_of_element_located(
            (By.CSS_SELECTOR, "input[id=saved-project-name]")
        )
    )
    driver.set_window_size(390, 844)
    driver.save_screenshot(str(artifacts / "project.png"))
    print(
        json.dumps(
            {
                "firefox": "passed",
                "uploads": 2,
                "reused": True,
                "export_bytes": exported.stat().st_size,
                "evidence": True,
                "reopened": True,
            }
        )
    )
except Exception:
    try:
        print(
            "Extension alerts:",
            [e.text for e in driver.find_elements(By.CSS_SELECTOR, "[role=alert]")],
        )
        driver.save_screenshot(str(artifacts / "failure.png"))
    except Exception:
        pass
    raise
finally:
    driver.quit()
