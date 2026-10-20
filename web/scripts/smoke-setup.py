"""First-run wizard smoke test (mock backend, fresh state). Needs `VITE_API_MODE=mock npx vite --port 3100`."""
import re, sys
from playwright.sync_api import sync_playwright

BASE = sys.argv[1] if len(sys.argv) > 1 else 'http://127.0.0.1:3100'
errors = []
with sync_playwright() as p:
    b = p.chromium.launch(); page = b.new_context(viewport={'width': 420, 'height': 900}).new_page()  # phone width on purpose
    page.on('console', lambda m: errors.append(f'console.{m.type}: {m.text}') if m.type == 'error' else None)
    page.on('pageerror', lambda e: errors.append(f'pageerror: {e}'))
    try:
        page.goto(f'{BASE}/?mock=fresh')
        page.wait_for_url(re.compile(r'/setup'))
        page.get_by_label('Setup token').fill('demo-setup-token')
        page.get_by_label('Email').fill('boss@corp.test')
        page.get_by_label('Password', exact=True).fill('a-long-password-1')
        page.get_by_label('Confirm password').fill('a-long-password-1')
        page.get_by_role('button', name='Create owner').click()
        page.wait_for_selector('svg[aria-label="Authenticator QR code"]')
        page.screenshot(path='/tmp/shot-setup-totp.png')
        page.get_by_label('6-digit code').fill('123456')
        page.get_by_role('button', name='Verify').click()
        page.wait_for_selector('text=Save these recovery codes')
        page.get_by_label('I have stored my recovery codes somewhere safe').check()
        page.get_by_role('button', name='Continue').click()
        page.get_by_label('Panel domain').fill('panel.example.com')
        page.get_by_role('button', name='Check DNS').click()
        page.get_by_role('button', name='Issue certificate').click()
        page.wait_for_selector('text=Issued', timeout=15000)
        page.get_by_role('button', name='Continue').click()
        page.get_by_label('Username').select_option('ops-deployer')
        page.get_by_role('button', name='Continue').click()
        page.wait_for_selector('text=Harden this server')
        page.wait_for_selector('text=Plan & apply', timeout=8000)
        page.screenshot(path='/tmp/shot-setup-harden.png')
        # no horizontal scroll at phone width
        assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth + 1'), 'page scrolls sideways at 420px'
        page.get_by_role('button', name='Finish setup').click()
        page.wait_for_url(re.compile(r':\d+/$'))
        page.wait_for_selector('header')
        page.screenshot(path='/tmp/shot-setup-done.png')
    except Exception as e:  # noqa: BLE001
        errors.append(f'FAILED: {e}'); page.screenshot(path='/tmp/shot-setup-fail.png')
    b.close()
if errors:
    print('SETUP SMOKE FAILED'); [print(' -', e) for e in errors]; sys.exit(1)
print('setup smoke ok')
