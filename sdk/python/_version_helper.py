"""Custom SCM version formatter for pdm-backend.

Produces PEP 440 compliant versions without local identifiers (e.g. +g<hash>),
which PyPI rejects.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from pdm.backend.hooks.version.scm import SCMVersion


def format_version(scm_version: SCMVersion) -> str:
    """Format SCMVersion as PEP 440 compliant string, no local parts."""
    if scm_version.distance is None:
        return str(scm_version.version)

    # Guess next version: 0.1.0 + distance 3 → 0.1.1.dev3
    from pdm.backend.hooks.version.scm import guess_next_version

    guessed = guess_next_version(scm_version.version)
    return f"{guessed}.dev{scm_version.distance}"
