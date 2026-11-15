"""Hand-written sample document, used to exercise the Doc wrapper + manifest."""

from doc import Doc

doc = Doc(OUT_PATH)

doc.heading("Quarterly Sales Report", "title")

doc.paragraph(
    "This report summarizes revenue across regions for the quarter. "
    "The table below breaks the totals down by region and product line. "
    "Select any element in the viewer and ask the assistant to change it; "
    "the assistant edits this script and re-renders the PDF.",
    "intro",
)

doc.paragraph(
    "A deliberately long paragraph to prove that an element split across "
    "two pages keeps the same id on every part. "
    + ("Revenue grew steadily across all regions this quarter. " * 120),
    "long-paragraph",
)

doc.table(
    [
        ["Region", "Revenue", "Growth"],
        ["North", "$120k", "+12%"],
        ["South", "$80k", "+8%"],
        ["East", "$95k", "+15%"],
        ["West", "$60k", "+5%"],
    ],
    "sales-table",
)

doc.save()
