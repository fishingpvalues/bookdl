#!/usr/bin/env python3
"""Render one Anna's Archive search page and print its HTML on stdout.

Anna's Archive is fronted by DDoS-Guard: a plain HTTP client gets a
302 -> 403 interstitial and a headless Chrome driven over CDP (chromedp)
never leaves the challenge page, even with --disable-blink-features=
AutomationControlled and the real chromium binary. Playwright launching the
same binary passes it, so the browser step lives here and the HTML is parsed
by the Go side (internal/anna/playwright.go) with selectors re-verified
against a live capture.

Usage: anna-search.py <query> [base_url_or_host]

stdout: the rendered search page HTML.
stderr + exit 1: the challenge never resolved or the page never rendered.
"""
import asyncio
import sys
import urllib.parse

DEFAULT_BASE = "https://annas-archive.gl"
USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
)
# The rendered page contains at least one result row with this class prefix, or
# the explicit "no files found" marker; either proves the challenge is past.
RESULT_MARKER = 'flex  pt-3 pb-3 border-b'


async def render(url: str, timeout_s: int = 60, wait_s: int = 45) -> str:
    from playwright.async_api import async_playwright
    from playwright_stealth import Stealth

    async with async_playwright() as p:
        browser = await p.chromium.launch(
            headless=True,
            executable_path="/usr/bin/chromium",
            args=[
                "--no-sandbox",
                "--disable-dev-shm-usage",
                "--disable-gpu",
                "--disable-blink-features=AutomationControlled",
            ],
        )
        try:
            context = await browser.new_context(
                user_agent=USER_AGENT, locale="en-US"
            )
            page = await context.new_page()
            await Stealth().apply_stealth_async(page)
            await page.goto(url, wait_until="domcontentloaded", timeout=timeout_s * 1000)

            # DDoS-Guard navigates the page by itself once the challenge is
            # through; content() raises while that navigation is in flight, so
            # retry until it settles instead of failing on the first error.
            for _ in range(wait_s):
                await asyncio.sleep(1)
                try:
                    await page.wait_for_load_state("domcontentloaded", timeout=15000)
                    body = await page.content()
                except Exception:
                    continue
                if "ddos" in (await page.title()).lower():
                    continue
                if RESULT_MARKER in body or "No files found" in body:
                    return body
            raise RuntimeError("DDoS-Guard never resolved or no result list")
        finally:
            await browser.close()


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    query = sys.argv[1]
    base = sys.argv[2] if len(sys.argv) > 2 else DEFAULT_BASE
    if "://" not in base:
        base = f"https://{base.strip('/')}"
    url = f"{base.rstrip('/')}/search?q={urllib.parse.quote_plus(query)}"
    try:
        body = asyncio.run(render(url))
    except Exception as exc:  # noqa: BLE001 - reported verbatim to the caller
        print(f"anna render failed: {exc}", file=sys.stderr)
        return 1
    sys.stdout.write(body)
    return 0


if __name__ == "__main__":
    sys.exit(main())
