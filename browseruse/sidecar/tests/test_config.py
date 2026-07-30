from __future__ import annotations

import os
import unittest
from unittest.mock import patch

from app.config import Settings


class SettingsTests(unittest.TestCase):
	def test_proxy_uses_http_proxy_when_no_browser_specific_value_is_set(self):
		with patch.dict(
			os.environ,
			{
				"BROWSER_USE_SIDECAR_TOKEN": "test-token",
				"http_proxy": "http://proxy.example:8080",
			},
			clear=True,
		):
			self.assertEqual(Settings.from_environment().proxy_url, "http://proxy.example:8080")

	def test_browser_specific_proxy_overrides_http_proxy(self):
		with patch.dict(
			os.environ,
			{
				"BROWSER_USE_SIDECAR_TOKEN": "test-token",
				"BROWSER_USE_PROXY_URL": "socks5://browser-proxy.example:1080",
				"http_proxy": "http://proxy.example:8080",
			},
			clear=True,
		):
			self.assertEqual(Settings.from_environment().proxy_url, "socks5://browser-proxy.example:1080")
