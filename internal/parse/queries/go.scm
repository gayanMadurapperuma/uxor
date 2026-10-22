; Capture conventions (shared by all languages):
;   @def.<kind> definition node, @name its name, @body optional body node
;   @call + @call.name [+ @call.qualifier]   call sites
;   @import                                   handed to the language hook
;   @extends / @implements                    base type name nodes
;   @export.name                              names explicitly exported
; Captures starting with "_" are predicate helpers and are ignored.

(function_declaration name: (identifier) @name body: (_)? @body) @def.function
(method_declaration name: (field_identifier) @name body: (_)? @body) @def.method

(type_spec name: (type_identifier) @name type: (interface_type)) @def.interface
(type_spec name: (type_identifier) @name) @def.type
(type_alias name: (type_identifier) @name) @def.type
(interface_type (method_elem name: (field_identifier) @name) @def.method)

(source_file (var_declaration (var_spec name: (identifier) @name) @def.var))
(source_file (var_declaration (var_spec_list (var_spec name: (identifier) @name) @def.var)))
(source_file (const_declaration (const_spec name: (identifier) @name) @def.var))

(field_declaration_list (field_declaration !name type: [(type_identifier) (qualified_type) (generic_type)] @extends))
(interface_type (type_elem [(type_identifier) (qualified_type)] @extends))

(call_expression function: (identifier) @call.name) @call
(call_expression
  function: (selector_expression operand: (_) @call.qualifier field: (field_identifier) @call.name)) @call
(composite_literal type: (type_identifier) @call.name) @call

(import_spec) @import
