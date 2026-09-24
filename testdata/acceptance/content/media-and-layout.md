# Media and Layout Acceptance

This acceptance-only member checks raster images, SVG import, code, a wide table, and a landscape section.

## PNG figure

![Synthetic PNG diagram](../../feasibility/synthetic-diagram.png)

## JPEG figure

![Synthetic JPEG diagram](../../feasibility/synthetic-diagram.jpg)

## SVG figure

![Synthetic SVG diagram](../../feasibility/synthetic-diagram.svg)

## Code sample

```go
func stableTarget(publicationID string) string {
    return "publication:" + publicationID
}
```

## Wide table

| Control | DOCX | Google | Owner | Evidence | Result |
|---|---|---|---|---|---|
| Cover | Required | Required | Release reviewer | Rendered first page | Pass |
| Sections | Required | Verify | Release reviewer | Portrait and landscape pages | Pass |
| Tables | Required | Verify | Release reviewer | Repeated header and wrapping | Pass |
| Figures | Required | Verify | Release reviewer | PNG, JPEG, and SVG | Pass |
| Links | Required | Verify | Release reviewer | Static TOC and heading links | Pass |
