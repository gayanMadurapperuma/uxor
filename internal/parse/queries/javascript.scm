; See go.scm for capture conventions.

(function_declaration name: (identifier) @name body: (_) @body) @def.function
(generator_function_declaration name: (identifier) @name body: (_) @body) @def.function
(class_declaration name: (identifier) @name body: (_) @body) @def.class
(method_definition name: (_) @name body: (_) @body) @def.method

(program
  (lexical_declaration
    (variable_declarator
      name: (identifier) @name
      value: [(arrow_function body: (_) @body) (function_expression body: (_) @body)])) @def.function)
(program
  (export_statement
    (lexical_declaration
      (variable_declarator
        name: (identifier) @name
        value: [(arrow_function body: (_) @body) (function_expression body: (_) @body)])) @def.function))
(program (lexical_declaration (variable_declarator name: (identifier) @name)) @def.var)
(program (export_statement (lexical_declaration (variable_declarator name: (identifier) @name)) @def.var))
(program (variable_declaration (variable_declarator name: (identifier) @name)) @def.var)

(call_expression
  function: (identifier) @_fn
  arguments: (arguments . (string (string_fragment) @name))
  (#match? @_fn "^(describe|it|test)$")) @def.test

(class_heritage (_) @extends)

(call_expression function: (identifier) @call.name) @call
(call_expression
  function: (member_expression object: (_) @call.qualifier property: (property_identifier) @call.name)) @call
(new_expression constructor: (identifier) @call.name) @call

(import_statement) @import
(export_statement source: (_)) @import
(call_expression
  function: (identifier) @_req
  arguments: (arguments . (string))
  (#eq? @_req "require")) @import
(call_expression function: (import) arguments: (arguments . (string))) @import

(export_statement (export_clause (export_specifier name: (_) @export.name)) !source)
(export_statement value: (identifier) @export.name)
(assignment_expression
  left: (member_expression object: (identifier) @_m property: (property_identifier) @_e)
  right: (object (shorthand_property_identifier) @export.name)
  (#eq? @_m "module") (#eq? @_e "exports"))
(assignment_expression
  left: (member_expression object: (identifier) @_m property: (property_identifier) @_e)
  right: (identifier) @export.name
  (#eq? @_m "module") (#eq? @_e "exports"))
(assignment_expression
  left: (member_expression object: (identifier) @_m property: (property_identifier) @export.name)
  (#eq? @_m "exports"))
