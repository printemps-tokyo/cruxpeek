# Security Policy

## Reporting a vulnerability

If you find a security issue, please do not open a public issue. Instead,
contact us at info@printemps.tokyo or use GitHub's private vulnerability
reporting ([Security advisories](../../security/advisories/new)).

Where possible, please include:

- Affected versions / environment
- Steps to reproduce
- Expected impact

We aim to send an initial response within 72 hours.

## A note on your API key

cruxpeek reads your CrUX API key from the `CRUX_API_KEY` environment variable
and sends it only to Google's `chromeuxreport.googleapis.com` endpoint as the
documented `?key=` query parameter. It never writes the key to stdout, stderr,
or any file, including error messages. Treat your key as a secret and prefer a
key restricted to the Chrome UX Report API.
