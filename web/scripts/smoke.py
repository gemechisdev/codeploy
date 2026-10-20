"""Browser smoke test against the mock backend: `VITE_API_MODE=mock npx vite --port 3100` must be running.
Walks login -> projects -> resource tabs -> security -> terminal and fails on any console error or uncaught exception."""
import re, sys, time
from playwright.sync_api import sync_playwright

BASE = sys.argv[1] if len(sys.argv) > 1 else 'http://127.0.0.1:3100'
errors = []

def main():
    with sync_playwright() as p:
        b = p.chromium.launch()
        ctx = b.new_context(viewport={'width': 1280, 'height': 800})
        page = ctx.new_page()
        page.on('console', lambda m: errors.append(f'console.{m.type}: {m.text}') if m.type in ('error',) else None)
        page.on('pageerror', lambda e: errors.append(f'pageerror: {e}'))
        page.goto(f'{BASE}/')
        page.wait_for_url(re.compile(r'/login'))
        page.get_by_label('Email').fill('owner@example.com')
        page.get_by_label('Password').fill('demo-password-change-me')
        page.get_by_role('button', name='Continue').click()
        page.get_by_label('Authenticator code').fill('123456')
        page.get_by_role('button', name='Verify and sign in').click()
        page.wait_for_selector('text=Dashboard', timeout=8000)
        page.screenshot(path='/tmp/shot-dashboard.png')
        page.get_by_role('link', name='Projects').first.click()
        page.wait_for_selector('h1:has-text("Projects")')
        page.screenshot(path='/tmp/shot-projects.png')
        page.locator('a[href^="/projects/"]').first.click()
        page.wait_for_selector('table')
        page.locator('a[href^="/resources/"]').first.click()
        page.wait_for_selector('text=Deployments')
        page.get_by_role('button', name='Details').first.click()
        page.wait_for_selector('[role=log]')
        page.wait_for_timeout(600)
        page.screenshot(path='/tmp/shot-deployment.png')
        page.keyboard.press('Escape')
        for tab in ['Overview', 'Environment', 'Limits', 'Isolation', 'Volumes', 'Logs']:
            page.get_by_role('tab', name=tab).click()
            page.wait_for_timeout(250)
        page.screenshot(path='/tmp/shot-env.png')
        page.goto(f'{BASE}/server/security') if False else page.get_by_role('link', name='Security').click()
        page.wait_for_selector('text=Host security')
        page.screenshot(path='/tmp/shot-security.png')
        page.get_by_role('link', name='Terminal').click()
        page.wait_for_selector('text=Host terminal')
        page.get_by_role('button', name='Connect').click()
        page.wait_for_selector('.xterm', timeout=8000)
        page.wait_for_timeout(800)
        page.keyboard.type('whoami')
        page.keyboard.press('Enter')
        page.wait_for_timeout(500)
        txt = page.locator('.xterm-rows').inner_text()
        assert 'ops-deployer' in txt, f'terminal did not echo/execute: {txt!r}'
        page.screenshot(path='/tmp/shot-terminal.png')
        b.close()

try:
    main()
except Exception as e:  # noqa: BLE001
    errors.append(f'FAILED: {e}')
if errors:
    print('SMOKE FAILED'); [print(' -', e) for e in errors]; sys.exit(1)
print('smoke ok')
