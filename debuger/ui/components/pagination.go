package components

import (
	"fmt"
	"net/url"
)

// PaginationConfig holds pagination configuration
type PaginationConfig struct {
	CurrentPage  int
	TotalItems   int
	ItemsPerPage int
	BaseURL      string
	QueryParams  url.Values
}

// DefaultItemsPerPage is the default number of items per page
const DefaultItemsPerPage = 50

// GetPaginationInfo calculates pagination information
func GetPaginationInfo(currentPage, totalItems, itemsPerPage int) (startIdx, endIdx, totalPages int) {
	if itemsPerPage <= 0 {
		itemsPerPage = DefaultItemsPerPage
	}
	if currentPage < 1 {
		currentPage = 1
	}

	totalPages = (totalItems + itemsPerPage - 1) / itemsPerPage
	if totalPages < 1 {
		totalPages = 1
	}

	if currentPage > totalPages {
		currentPage = totalPages
	}

	startIdx = (currentPage - 1) * itemsPerPage
	endIdx = startIdx + itemsPerPage
	if endIdx > totalItems {
		endIdx = totalItems
	}

	return startIdx, endIdx, totalPages
}

// Pagination generates Bootstrap pagination HTML
func Pagination(config PaginationConfig) string {
	if config.ItemsPerPage <= 0 {
		config.ItemsPerPage = DefaultItemsPerPage
	}

	_, _, totalPages := GetPaginationInfo(config.CurrentPage, config.TotalItems, config.ItemsPerPage)

	if totalPages <= 1 {
		return "" // No pagination needed
	}

	// Build query string helper
	buildURL := func(page int) string {
		params := url.Values{}
		if config.QueryParams != nil {
			for k, v := range config.QueryParams {
				for _, val := range v {
					params.Add(k, val)
				}
			}
		}
		params.Set("page", fmt.Sprintf("%d", page))
		return config.BaseURL + "?" + params.Encode()
	}

	html := `<nav aria-label="Page navigation" class="mt-4">
    <ul class="pagination justify-content-center flex-wrap">`

	// Previous button
	if config.CurrentPage > 1 {
		html += fmt.Sprintf(`
        <li class="page-item">
            <a class="page-link" href="%s" aria-label="Previous">
                <span aria-hidden="true">&laquo;</span>
            </a>
        </li>`, buildURL(config.CurrentPage-1))
	} else {
		html += `
        <li class="page-item disabled">
            <span class="page-link">&laquo;</span>
        </li>`
	}

	// Page numbers with ellipsis
	startPage := 1
	endPage := totalPages
	maxVisiblePages := 7

	if totalPages > maxVisiblePages {
		// Show first, last, and pages around current
		if config.CurrentPage <= 4 {
			endPage = 5
		} else if config.CurrentPage >= totalPages-3 {
			startPage = totalPages - 4
		} else {
			startPage = config.CurrentPage - 2
			endPage = config.CurrentPage + 2
		}
	}

	// First page + ellipsis if needed
	if startPage > 1 {
		html += fmt.Sprintf(`
        <li class="page-item">
            <a class="page-link" href="%s">1</a>
        </li>`, buildURL(1))
		if startPage > 2 {
			html += `
        <li class="page-item disabled">
            <span class="page-link">...</span>
        </li>`
		}
	}

	// Page numbers
	for i := startPage; i <= endPage; i++ {
		if i == config.CurrentPage {
			html += fmt.Sprintf(`
        <li class="page-item active">
            <span class="page-link">%d</span>
        </li>`, i)
		} else {
			html += fmt.Sprintf(`
        <li class="page-item">
            <a class="page-link" href="%s">%d</a>
        </li>`, buildURL(i), i)
		}
	}

	// Last page + ellipsis if needed
	if endPage < totalPages {
		if endPage < totalPages-1 {
			html += `
        <li class="page-item disabled">
            <span class="page-link">...</span>
        </li>`
		}
		html += fmt.Sprintf(`
        <li class="page-item">
            <a class="page-link" href="%s">%d</a>
        </li>`, buildURL(totalPages), totalPages)
	}

	// Next button
	if config.CurrentPage < totalPages {
		html += fmt.Sprintf(`
        <li class="page-item">
            <a class="page-link" href="%s" aria-label="Next">
                <span aria-hidden="true">&raquo;</span>
            </a>
        </li>`, buildURL(config.CurrentPage+1))
	} else {
		html += `
        <li class="page-item disabled">
            <span class="page-link">&raquo;</span>
        </li>`
	}

	html += `
    </ul>
</nav>`

	// Add page info text
	startItem := (config.CurrentPage-1)*config.ItemsPerPage + 1
	endItem := startItem + config.ItemsPerPage - 1
	if endItem > config.TotalItems {
		endItem = config.TotalItems
	}

	html += fmt.Sprintf(`
<div class="text-center text-muted mb-3">
    <small>Showing %d-%d of %d items (Page %d of %d)</small>
</div>`, startItem, endItem, config.TotalItems, config.CurrentPage, totalPages)

	return html
}

// PaginationSimple generates a simple pagination without query params
func PaginationSimple(currentPage, totalItems, itemsPerPage int, baseURL string) string {
	return Pagination(PaginationConfig{
		CurrentPage:  currentPage,
		TotalItems:   totalItems,
		ItemsPerPage: itemsPerPage,
		BaseURL:      baseURL,
	})
}
