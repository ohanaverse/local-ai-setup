<!--
  Extracted from Claude Code v2.1.278
  Source offset: 173997252
  Content hash: dd93d50c893c2212
  Category: charts
  Auto-generated — do not edit manually
-->

:""}
- This tool can read Jupyter notebooks (.ipynb files) and returns all cells with their outputs, combining code, text, and visualizations.
- This tool can only read files, not directories. To list files in a directory, use the registered shell tool.
- You will regularly be asked to read screenshots. If the user provides a path to a screenshot, ALWAYS use this tool to view the file at the path. This tool will work with all temporary file paths.
- If you read a file that exists but has empty contents you will receive a system reminder warning in place of file contents.