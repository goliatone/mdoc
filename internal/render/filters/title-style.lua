function Header(element)
  element.identifier = ''

  if element.level == 1 then
    local attributes = pandoc.Attr('', {}, {['custom-style'] = 'Title'})
    return pandoc.Div({pandoc.Para(element.content)}, attributes)
  end

  element.level = element.level - 1
  return element
end
