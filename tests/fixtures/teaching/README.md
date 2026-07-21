# Phase 2 generated teaching fixtures

This directory intentionally contains no binary fixtures and no antivirus test signature.
`scripts/generate-phase2-fixtures.sh` creates the complete set inside the disposable Linux
acceptance environment and the runner removes its dedicated volume afterward.

Generated files cover:

- a safe DOCX and replacement DOCX;
- a short MP4 and a non-browser-playable Matroska video;
- a multipart-resume payload larger than 8 MiB;
- an archive, macro-enabled document, declared/content type mismatch, and EICAR scanner probe.

Never commit the generated directory or copy the EICAR fixture outside the disposable test
resource. The acceptance runner treats failure to detect the scanner probe as a failed gate.
