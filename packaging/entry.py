"""Bundle entry point.

PyInstaller runs its target as __main__, which breaks the relative imports in
livecap.app. Importing the package here keeps them working.
"""

from livecap.app import main

raise SystemExit(main())
